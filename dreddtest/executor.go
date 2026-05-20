// Package dreddtest provides in-process test helpers that satisfy the
// worker.Executor interface without booting a real Firecracker MicroVM.
//
// LocalExecutor runs the language's CompileCmd/RunCmd directly on the host
// using /bin/sh -c. It is suitable only for tests — there is no isolation.
// FlakyExecutor wraps any Executor and fails the first N calls with a
// VM-level error, exercising the worker's single-retry path.
package dreddtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ondbyte/dredd/agent"
)

// LocalExecutor runs user code directly on the host using /bin/sh. It is
// intended for integration tests where you want to exercise the queue,
// HTTP API and worker without the Firecracker boot path.
type LocalExecutor struct{}

func (LocalExecutor) Execute(ctx context.Context, _ string, req *agent.ExecRequest) (*agent.ExecResponse, error) {
	work, err := os.MkdirTemp("", "dreddtest-work-")
	if err != nil {
		return nil, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(work)

	if err := os.WriteFile(filepath.Join(work, req.SourceFile), []byte(req.Source), 0o644); err != nil {
		return &agent.ExecResponse{AgentError: "write source: " + err.Error()}, nil
	}

	if req.CompileCmd != "" {
		stdout, code, stderr, _ := runShell(ctx, work, req.CompileCmd, "", req.TimeLimitMs*4, req.OutputLimitBytes)
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
		stdout, code, stderr, timedOut := runShell(ctx, work, req.RunCmd, in, req.TimeLimitMs, req.OutputLimitBytes)
		results = append(results, agent.CaseResult{
			Stdout:   string(stdout),
			Stderr:   string(stderr),
			ExitCode: code,
			TimedOut: timedOut,
		})
	}
	return &agent.ExecResponse{Results: results}, nil
}

func runShell(parent context.Context, workDir, shellCmd, stdin string, timeoutMs, outCap int) ([]byte, int, []byte, bool) {
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", shellCmd)
	cmd.Dir = workDir
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

// FlakyExecutor wraps an underlying Executor and fails its first N calls
// with a synthetic VM error. Used to exercise the worker's retry path.
type FlakyExecutor struct {
	Inner       interface {
		Execute(ctx context.Context, langID string, req *agent.ExecRequest) (*agent.ExecResponse, error)
	}
	FailuresLeft atomic.Int32
}

func NewFlakyExecutor(inner interface {
	Execute(ctx context.Context, langID string, req *agent.ExecRequest) (*agent.ExecResponse, error)
}, failures int32) *FlakyExecutor {
	f := &FlakyExecutor{Inner: inner}
	f.FailuresLeft.Store(failures)
	return f
}

func (f *FlakyExecutor) Execute(ctx context.Context, langID string, req *agent.ExecRequest) (*agent.ExecResponse, error) {
	if f.FailuresLeft.Add(-1) >= 0 {
		return nil, errors.New("simulated VM failure")
	}
	return f.Inner.Execute(ctx, langID, req)
}
