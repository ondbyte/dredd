package dreddtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/ondbyte/dredd/agent"
)

// DockerExecutor runs each ExecRequest inside a transient Docker container
// keyed on a per-language image. It mirrors dredd's production architecture
// (one isolated environment per language) but uses Docker instead of
// Firecracker so it can run on any host with the Docker daemon.
//
// For every request:
//  1. A scratch work directory is created on the host and the source file
//     is written there.
//  2. If CompileCmd is set, one `docker run` invocation compiles in /work.
//  3. For each Stdins entry, another `docker run` runs RunCmd with that
//     stdin.
//
// The work directory is bind-mounted to /work inside the container; the
// container runs as the host UID/GID so files it creates are owned by the
// host caller and the work directory cleans up.
type DockerExecutor struct {
	// Images maps each language ID to the docker image to use.
	Images map[string]string

	// DockerBin lets you point at a podman / nerdctl / etc. Defaults to "docker".
	DockerBin string

	// Network is the docker --network flag value. Defaults to "none" for
	// isolation; some languages (TypeScript, Clojure dep fetches) need
	// network and override per-case via PerImageNetwork.
	Network string

	// PerImageNetwork overrides Network for specific image:network pairs.
	// Useful for the few languages that fetch deps at compile time.
	PerImageNetwork map[string]string

	// PullTimeout caps how long an initial `docker pull` may take.
	PullTimeout time.Duration

	// pulled tracks which images we've already verified are present locally.
	// Guarded by ensureMu so concurrent Execute calls don't race the map
	// and don't kick off duplicate `docker pull` commands.
	ensureMu sync.Mutex
	pulled   map[string]bool
}

// NewDockerExecutor builds an executor with sensible defaults.
func NewDockerExecutor(images map[string]string) *DockerExecutor {
	return &DockerExecutor{
		Images:      images,
		DockerBin:   "docker",
		Network:     "none",
		PullTimeout: 5 * time.Minute,
		pulled:      make(map[string]bool),
	}
}

// Execute implements worker.Executor.
func (e *DockerExecutor) Execute(ctx context.Context, langID string, req *agent.ExecRequest) (*agent.ExecResponse, error) {
	image, ok := e.Images[langID]
	if !ok || image == "" {
		return nil, fmt.Errorf("docker executor: no image for language %q", langID)
	}
	if err := e.ensureImage(ctx, image); err != nil {
		return nil, fmt.Errorf("ensure image %s: %w", image, err)
	}

	work, err := os.MkdirTemp("", "dredd-doc-")
	if err != nil {
		return nil, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(work)
	// Ensure container processes can read/write /work as host user.
	_ = os.Chmod(work, 0o777)

	if err := os.WriteFile(filepath.Join(work, req.SourceFile), []byte(req.Source), 0o644); err != nil {
		return &agent.ExecResponse{AgentError: "write source: " + err.Error()}, nil
	}

	network := e.Network
	if e.PerImageNetwork != nil {
		if n, ok := e.PerImageNetwork[image]; ok {
			network = n
		}
	}

	if req.CompileCmd != "" {
		stdout, code, stderr, _ := e.runDocker(ctx, image, work, network, req.CompileCmd, "", req.TimeLimitMs*4, req.OutputLimitBytes)
		if code != 0 {
			msg := string(stderr)
			if msg == "" {
				msg = string(stdout)
			}
			return &agent.ExecResponse{CompileError: msg}, nil
		}
	}

	results := make([]agent.CaseResult, 0, len(req.Stdins))
	for _, in := range req.Stdins {
		stdout, code, stderr, timedOut := e.runDocker(ctx, image, work, network, req.RunCmd, in, req.TimeLimitMs, req.OutputLimitBytes)
		results = append(results, agent.CaseResult{
			Stdout:   string(stdout),
			Stderr:   string(stderr),
			ExitCode: code,
			TimedOut: timedOut,
		})
	}
	return &agent.ExecResponse{Results: results}, nil
}

// PrewarmImages pulls every image referenced by Images so that subsequent
// Execute calls don't pay first-pull latency. Returns the first pull error
// encountered (or nil).
func (e *DockerExecutor) PrewarmImages(ctx context.Context) error {
	seen := map[string]struct{}{}
	for _, img := range e.Images {
		if _, ok := seen[img]; ok {
			continue
		}
		seen[img] = struct{}{}
		if err := e.ensureImage(ctx, img); err != nil {
			return fmt.Errorf("pull %s: %w", img, err)
		}
	}
	return nil
}

// MissingImages returns images referenced by the catalogue that are not
// present locally (neither pre-pulled nor pullable). Useful for failing a
// test early with a clear list of what is missing.
func (e *DockerExecutor) MissingImages(ctx context.Context) ([]string, error) {
	seen := map[string]struct{}{}
	var missing []string
	for _, img := range e.Images {
		if _, ok := seen[img]; ok {
			continue
		}
		seen[img] = struct{}{}
		if e.localHas(ctx, img) {
			continue
		}
		// Try to pull; if pull fails, the image is missing.
		if err := e.pull(ctx, img); err != nil {
			missing = append(missing, img)
		}
	}
	return missing, nil
}

// ensureImage verifies an image is locally available, pulling it once if
// not. Concurrent callers serialize on ensureMu so the map stays
// race-free and duplicate `docker pull` invocations don't fire.
func (e *DockerExecutor) ensureImage(ctx context.Context, image string) error {
	e.ensureMu.Lock()
	defer e.ensureMu.Unlock()
	if e.pulled[image] {
		return nil
	}
	if e.localHas(ctx, image) {
		e.pulled[image] = true
		return nil
	}
	if err := e.pull(ctx, image); err != nil {
		return err
	}
	e.pulled[image] = true
	return nil
}

func (e *DockerExecutor) localHas(ctx context.Context, image string) bool {
	cmd := exec.CommandContext(ctx, e.DockerBin, "image", "inspect", image)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

func (e *DockerExecutor) pull(ctx context.Context, image string) error {
	timeout := e.PullTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, e.DockerBin, "pull", "--quiet", image)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, stderr.String())
	}
	return nil
}

func (e *DockerExecutor) runDocker(parent context.Context, image, workDir, network, shellCmd, stdin string, timeoutMs, outCap int) ([]byte, int, []byte, bool) {
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	uid := os.Getuid()
	gid := os.Getgid()

	args := []string{
		"run", "--rm", "-i",
		"--network", network,
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"-v", workDir + ":/work",
		"-w", "/work",
		"-e", "HOME=/tmp",
		image,
		"/bin/sh", "-c", shellCmd,
	}
	cmd := exec.CommandContext(ctx, e.DockerBin, args...)
	cmd.Stdin = bytes.NewBufferString(stdin)

	var stdout, stderr cappedBuffer
	stdout.cap = outCap
	stderr.cap = outCap
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return stdout.Bytes(), code, stderr.Bytes(), timedOut
}
