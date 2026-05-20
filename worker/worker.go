// Package worker consumes jobs from the Redis queue and dispatches them
// through an Executor (typically a Firecracker-backed VM pool) onto a
// MicroVM.
//
// It implements the single-retry-on-VM-failure requirement: if the Executor
// returns an error (which it must reserve for VM/transport/boot failures),
// the job is moved onto a fresh VM exactly once before being marked failed.
// A successful Executor call always finalises the job — even if the response
// carries CompileError or AgentError.
package worker

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ondbyte/dredd/agent"
	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/jobs"
	"github.com/ondbyte/dredd/langs"
	"github.com/ondbyte/dredd/queue"
)

// Executor is the seam the worker depends on. Production wiring uses
// pool.NewExecutor (which acquires a VM from a pool and talks vsock to
// the guest agent); tests can plug in any implementation
// (dreddtest.LocalExecutor, dreddtest.DockerExecutor, dreddtest.FlakyExecutor).
//
// Contract:
//   - A non-nil error means the request never produced a useful result.
//     The worker treats this as a transport / VM-side failure and retries
//     on a fresh executor call exactly once before marking the job failed.
//   - A nil error means user code was actually executed. Compile errors
//     and per-case results go inside the ExecResponse — the worker will
//     not retry, even if every case failed.
//
// LocalExecutor and DockerExecutor never return errors in normal use, so
// the retry path is only exercised by Firecracker-backed pools and
// FlakyExecutor in tests.
type Executor interface {
	Execute(ctx context.Context, langID string, req *agent.ExecRequest) (*agent.ExecResponse, error)
}

type Worker struct {
	cfg *config.Config
	q   *queue.Queue
	reg *langs.Registry
	exe Executor
}

func New(cfg *config.Config, q *queue.Queue, reg *langs.Registry, exe Executor) *Worker {
	return &Worker{cfg: cfg, q: q, reg: reg, exe: exe}
}

// Run blocks until ctx is cancelled. It spawns cfg.WorkerConcurrency goroutines.
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < w.cfg.WorkerConcurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			w.loop(ctx, id)
		}(i)
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context, id int) {
	for {
		if ctx.Err() != nil {
			return
		}
		jobID, err := w.q.Dequeue(ctx)
		if err != nil {
			log.Printf("worker[%d] dequeue: %v", id, err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if jobID == "" {
			continue
		}
		w.process(ctx, jobID)
	}
}

func (w *Worker) process(ctx context.Context, jobID string) {
	job, err := w.q.Load(ctx, jobID)
	if err != nil {
		// Redis transient errors are logged; the job will be re-dequeued
		// next iteration if it's still on the queue list. Permanent
		// corruption is surfaced by Load returning a parse error.
		log.Printf("worker: load %s: %v", jobID, err)
		return
	}
	if err := w.q.SetRunning(ctx, jobID); err != nil {
		// If we can't even mark the job running, Redis is sick. Bail —
		// retrying would just rack up failed Finish writes and lose the
		// result. The job stays in whatever state Redis last persisted.
		log.Printf("worker: setRunning %s: %v (abandoning attempt)", jobID, err)
		return
	}
	lang, ok := w.reg.Get(job.Language)
	if !ok {
		_ = w.q.Finish(ctx, jobID, jobs.StatusFailed, "", fmt.Sprintf("unknown language %q", job.Language), nil)
		return
	}

	req := &agent.ExecRequest{
		SourceFile:       lang.SourceFile,
		Source:           job.Source,
		CompileCmd:       lang.CompileCmd,
		RunCmd:           lang.RunCmd,
		Stdins:           job.Stdins,
		TimeLimitMs:      pickPositive(job.TimeLimitMs, int(w.cfg.DefaultTimeLimit/time.Millisecond)),
		MemoryLimitMb:    pickPositive(job.MemoryLimitMb, w.cfg.DefaultMemoryLimit),
		OutputLimitBytes: w.cfg.OutputLimitBytes,
	}

	const maxAttempts = 2
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := w.exe.Execute(ctx, lang.ID, req)
		if err == nil {
			if resp.AgentError != "" {
				_ = w.q.Finish(ctx, jobID, jobs.StatusFailed, "", "agent: "+resp.AgentError, nil)
				return
			}
			_ = w.q.Finish(ctx, jobID, jobs.StatusDone, resp.CompileError, "", resp.Results)
			return
		}
		lastErr = err
		log.Printf("worker: attempt %d/%d for job %s failed: %v", attempt, maxAttempts, jobID, err)
	}
	_ = w.q.Finish(ctx, jobID, jobs.StatusFailed, "", lastErr.Error(), nil)
}

func pickPositive(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
