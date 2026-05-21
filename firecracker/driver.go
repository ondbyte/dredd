package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

// VM is a handle to a running Firecracker process.
type VM struct {
	ID         string
	LanguageID string // set when this VM is tied to a language's rootfs
	WorkDir    string
	APISocket  string
	VsockUDS   string
	cmd        *exec.Cmd
}

// BootOptions configures one VM boot.
type BootOptions struct {
	FirecrackerBin string
	KernelPath     string
	RootfsPath     string
	WorkDir        string // unique tmp dir for this VM's sockets
	Vcpus          int
	MemMB          int
	LanguageID     string
}

// Driver boots and tracks Firecracker MicroVMs.
type Driver struct {
	bin string
	seq atomic.Uint64
}

func NewDriver(firecrackerBin string) *Driver {
	return &Driver{bin: firecrackerBin}
}

func (d *Driver) nextID() string {
	return fmt.Sprintf("vm-%d-%d", os.Getpid(), d.seq.Add(1))
}

// Boot launches a new MicroVM and returns once it has acknowledged the
// InstanceStart action over the API. It does NOT wait for the guest agent;
// callers should subsequently VsockHostDial.
func (d *Driver) Boot(ctx context.Context, opt BootOptions) (*VM, error) {
	if opt.FirecrackerBin == "" {
		opt.FirecrackerBin = d.bin
	}
	if err := os.MkdirAll(opt.WorkDir, 0o700); err != nil {
		return nil, err
	}
	id := d.nextID()
	apiSocket := filepath.Join(opt.WorkDir, "fc.sock")
	vsockUDS := filepath.Join(opt.WorkDir, "vsock.sock")

	// Pre-clean stale sockets (firecracker refuses to bind if they exist).
	_ = os.Remove(apiSocket)
	_ = os.Remove(vsockUDS)

	cmd := exec.Command(opt.FirecrackerBin, "--api-sock", apiSocket, "--id", id)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start firecracker: %w", err)
	}

	vm := &VM{
		ID:         id,
		LanguageID: opt.LanguageID,
		WorkDir:    opt.WorkDir,
		APISocket:  apiSocket,
		VsockUDS:   vsockUDS,
		cmd:        cmd,
	}

	if err := waitForSocket(ctx, apiSocket, 5*time.Second); err != nil {
		vm.Kill()
		return nil, err
	}

	api := newAPI(apiSocket)
	if err := api.put(ctx, "/machine-config", map[string]any{
		"vcpu_count":  opt.Vcpus,
		"mem_size_mib": opt.MemMB,
	}); err != nil {
		vm.Kill()
		return nil, fmt.Errorf("machine-config: %w", err)
	}
	if err := api.put(ctx, "/boot-source", map[string]any{
		"kernel_image_path": opt.KernelPath,
		"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/dreddagent-init",
	}); err != nil {
		vm.Kill()
		return nil, fmt.Errorf("boot-source: %w", err)
	}
	// Give the VM its own writable copy of the rootfs. Firecracker mounts
	// the drive r/w, and dreddagent + user code will both write to it
	// (compile output, /work/main.<ext>, /tmp scratch). Sharing the
	// pristine file would (a) accumulate state from previous runs and
	// (b) leave the ext4 journal in a "needs recovery" state after each
	// VM kill — the next mount logs `mounting fs with errors`, and on
	// some images (node, ruby, anything that hits /tmp during init) the
	// recovery hangs long enough that the agent never gets to listen
	// on vsock. `cp --reflink=auto` keeps this near-free on btrfs/xfs;
	// on ext4 we pay the full file size, but that's still cheaper than
	// hitting the dirty-journal path.
	perVMRootfs := filepath.Join(opt.WorkDir, "rootfs.ext4")
	if err := cloneFile(opt.RootfsPath, perVMRootfs); err != nil {
		vm.Kill()
		return nil, fmt.Errorf("clone rootfs: %w", err)
	}
	if err := api.put(ctx, "/drives/rootfs", map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   perVMRootfs,
		"is_root_device": true,
		"is_read_only":   false,
	}); err != nil {
		vm.Kill()
		return nil, fmt.Errorf("drives: %w", err)
	}
	if err := api.put(ctx, "/vsock", map[string]any{
		"guest_cid": 3,
		"uds_path":  vsockUDS,
	}); err != nil {
		vm.Kill()
		return nil, fmt.Errorf("vsock: %w", err)
	}
	if err := api.put(ctx, "/actions", map[string]any{
		"action_type": "InstanceStart",
	}); err != nil {
		vm.Kill()
		return nil, fmt.Errorf("InstanceStart: %w", err)
	}

	return vm, nil
}

// Kill terminates the VM process and removes its working directory.
func (vm *VM) Kill() {
	if vm == nil {
		return
	}
	if vm.cmd != nil && vm.cmd.Process != nil {
		_ = vm.cmd.Process.Kill()
		_, _ = vm.cmd.Process.Wait()
	}
	if vm.WorkDir != "" {
		_ = os.RemoveAll(vm.WorkDir)
	}
}

// --- internal API helper ---

type apiClient struct {
	sock string
	hc   *http.Client
}

func newAPI(sock string) *apiClient {
	return &apiClient{
		sock: sock,
		hc: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", sock)
				},
			},
			Timeout: 5 * time.Second,
		},
	}
}

func (c *apiClient) put(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://unix"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("firecracker API %s -> %s", path, resp.Status)
	}
	return nil
}

// cloneFile copies src to dst, preferring `cp --reflink=auto` so that
// on COW-capable filesystems (btrfs, xfs, recent ext4 with `mkfs.ext4
// -O reflink`) we pay no allocation cost and can boot in milliseconds.
// Falls back to a streaming copy when reflink isn't supported.
func cloneFile(src, dst string) error {
	if err := exec.Command("cp", "--reflink=auto", "--sparse=always", src, dst).Run(); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func waitForSocket(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for firecracker api socket")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
