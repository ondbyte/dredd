// Command dreddagent is the guest-side init binary that ships inside every
// dredd rootfs.
//
// On boot it:
//   1. Mounts /proc, /sys, /dev (best-effort).
//   2. Starts a vsock listener on agent.VsockPort.
//   3. Accepts exactly one ExecRequest, runs CompileCmd once, then runs
//      RunCmd against each Stdins entry sequentially.
//   4. Sends the ExecResponse back and powers the VM off.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"github.com/ondbyte/dredd/agent"
)

const workDir = "/work"

func main() {
	_ = syscall.Mount("proc", "/proc", "proc", 0, "")
	_ = syscall.Mount("sysfs", "/sys", "sysfs", 0, "")
	_ = syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
	_ = os.MkdirAll(workDir, 0o755)

	if err := serve(); err != nil {
		log.Printf("dreddagent: %v", err)
	}
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(0)
}

func serve() error {
	ln, err := vsock.Listen(agent.VsockPort, nil)
	if err != nil {
		return fmt.Errorf("vsock listen: %w", err)
	}
	defer ln.Close()

	conn, err := ln.Accept()
	if err != nil {
		return fmt.Errorf("accept: %w", err)
	}
	defer conn.Close()

	req, err := readFrame(conn)
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	resp := handle(req)
	if err := writeFrame(conn, resp); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	time.Sleep(50 * time.Millisecond)
	return nil
}

func handle(req *agent.ExecRequest) *agent.ExecResponse {
	srcPath := filepath.Join(workDir, req.SourceFile)
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		return &agent.ExecResponse{AgentError: "mkdir: " + err.Error()}
	}
	if err := os.WriteFile(srcPath, []byte(req.Source), 0o644); err != nil {
		return &agent.ExecResponse{AgentError: "write source: " + err.Error()}
	}

	if req.CompileCmd != "" {
		stdout, code, stderr, _, err := runOnce(req.CompileCmd, "", req.TimeLimitMs*4, req.MemoryLimitMb, req.OutputLimitBytes)
		if err != nil && code == 0 {
			return &agent.ExecResponse{AgentError: "compile: " + err.Error()}
		}
		if code != 0 {
			msg := string(stderr)
			if msg == "" {
				msg = string(stdout)
			}
			return &agent.ExecResponse{CompileError: msg}
		}
	}

	results := make([]agent.CaseResult, 0, len(req.Stdins))
	for _, in := range req.Stdins {
		stdout, code, stderr, timedOut, err := runOnce(req.RunCmd, in, req.TimeLimitMs, req.MemoryLimitMb, req.OutputLimitBytes)
		if err != nil && code == 0 && !timedOut {
			return &agent.ExecResponse{AgentError: "exec: " + err.Error()}
		}
		results = append(results, agent.CaseResult{
			Stdout:   string(stdout),
			Stderr:   string(stderr),
			ExitCode: code,
			TimedOut: timedOut,
		})
	}
	return &agent.ExecResponse{Results: results}
}

// runOnce runs cmd via /bin/sh -c. Returns stdout, exit code, stderr,
// timedOut flag, error.
func runOnce(shellCmd, stdin string, timeoutMs, memMB, outCap int) ([]byte, int, []byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", shellCmd)
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewBufferString(stdin)

	var stdout, stderr cappedBuffer
	stdout.cap = outCap
	stderr.cap = outCap
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	err := cmd.Run()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if timedOut {
			code = -1
		} else {
			code = -1
		}
	}
	_ = memMB // memory limits enforced via firecracker /machine-config cap; per-case rlimits skipped for v1
	return stdout.Bytes(), code, stderr.Bytes(), timedOut, err
}

type cappedBuffer struct {
	cap int
	buf bytes.Buffer
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.cap <= 0 || c.buf.Len() >= c.cap {
		return len(p), nil
	}
	room := c.cap - c.buf.Len()
	if room >= len(p) {
		return c.buf.Write(p)
	}
	c.buf.Write(p[:room])
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }

func readFrame(r io.Reader) (*agent.ExecRequest, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > agent.MaxFrameBytes {
		return nil, fmt.Errorf("invalid frame size %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	var req agent.ExecRequest
	if err := json.Unmarshal(buf, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func writeFrame(w io.Writer, resp *agent.ExecResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
