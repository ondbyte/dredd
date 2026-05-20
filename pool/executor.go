package pool

import (
	"context"
	"fmt"
	"time"

	"github.com/ondbyte/dredd/agent"
	"github.com/ondbyte/dredd/firecracker"
)

// Executor adapts a VMPool to the worker.Executor interface. It is the
// production wiring: acquire a VM, vsock-dial the guest agent, exchange
// one request/response, then either Release (success) or Discard (failure).
type Executor struct {
	Pool         VMPool
	DialTimeout  time.Duration // default 15s
	ExtraBudget  time.Duration // grace beyond timeLimit*(n+1); default 30s
	VsockPort    uint32        // default agent.VsockPort
}

// NewExecutor returns an Executor backed by the given pool with sensible
// defaults.
func NewExecutor(p VMPool) *Executor {
	return &Executor{
		Pool:        p,
		DialTimeout: 15 * time.Second,
		ExtraBudget: 30 * time.Second,
		VsockPort:   agent.VsockPort,
	}
}

func (e *Executor) Execute(ctx context.Context, langID string, req *agent.ExecRequest) (*agent.ExecResponse, error) {
	vm, err := e.Pool.Acquire(ctx, langID)
	if err != nil {
		return nil, fmt.Errorf("acquire: %w", err)
	}
	resp, exchErr := e.exchange(vm, req)
	if exchErr != nil {
		e.Pool.Discard(vm)
		return nil, exchErr
	}
	e.Pool.Release(vm)
	return resp, nil
}

func (e *Executor) exchange(vm *firecracker.VM, req *agent.ExecRequest) (*agent.ExecResponse, error) {
	budget := time.Duration(req.TimeLimitMs)*time.Millisecond*time.Duration(len(req.Stdins)+1) + e.ExtraBudget
	conn, err := firecracker.VsockHostDial(vm.VsockUDS, e.VsockPort, e.DialTimeout)
	if err != nil {
		return nil, fmt.Errorf("vsock dial: %w", err)
	}
	defer conn.Close()
	return firecracker.Exchange(conn, req, budget)
}
