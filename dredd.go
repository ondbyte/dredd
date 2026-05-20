// Package dredd is the top-level entry point for embedding dredd in another
// Go program. It wires the language registry, Redis queue, VM pool / Executor,
// worker, and HTTP server into a single App.
//
// Typical use:
//
//	app, err := dredd.New(dredd.Options{
//	    Config:   cfg, // from config.FromEnv()
//	    Registry: reg, // from langs.Load(...)
//	    Redis:    rdb, // *redis.Client
//	    // Executor: optional — defaults to a Firecracker-backed pool.
//	})
//	if err != nil { ... }
//	go app.Run(ctx)
//	defer app.Shutdown(context.Background())
//
// External callers may also provide their own worker.Executor (for example,
// to substitute the Firecracker driver in tests or to plug in a different
// isolation backend).
package dredd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ondbyte/dredd/api"
	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/firecracker"
	"github.com/ondbyte/dredd/langs"
	"github.com/ondbyte/dredd/pool"
	"github.com/ondbyte/dredd/queue"
	"github.com/ondbyte/dredd/worker"
	"github.com/redis/go-redis/v9"
)

// Options configures an App.
type Options struct {
	// Config is required. Use config.FromEnv() to populate it.
	Config *config.Config
	// Registry is required. Use langs.Load() to populate it.
	Registry *langs.Registry
	// Redis is required. Caller owns the client lifecycle.
	Redis *redis.Client
	// Executor is optional. If nil, dredd builds a Firecracker-backed pool
	// from Config and uses pool.NewExecutor.
	Executor worker.Executor
}

// App is a fully wired dredd instance.
type App struct {
	opts     Options
	queue    *queue.Queue
	pool     pool.VMPool // nil if Executor was provided externally
	worker   *worker.Worker
	httpSrv  *http.Server
	listener net.Listener
}

// New validates options and constructs the wiring but does not start
// anything.
func New(o Options) (*App, error) {
	if o.Config == nil {
		return nil, errors.New("dredd: Config is required")
	}
	if o.Registry == nil {
		return nil, errors.New("dredd: Registry is required")
	}
	if o.Redis == nil {
		return nil, errors.New("dredd: Redis is required")
	}

	q := queue.New(o.Redis, o.Config.ResultTTL)

	var (
		exe   worker.Executor
		vmPool pool.VMPool
	)
	if o.Executor != nil {
		exe = o.Executor
	} else {
		drv := firecracker.NewDriver(o.Config.FirecrackerBin)
		p, err := pool.New(o.Config, drv, o.Registry)
		if err != nil {
			return nil, fmt.Errorf("dredd: build pool: %w", err)
		}
		vmPool = p
		exe = pool.NewExecutor(p)
	}

	w := worker.New(o.Config, q, o.Registry, exe)
	srv := api.NewServer(o.Config, o.Registry, q)

	// Bind eagerly so callers can read the resolved address (useful when
	// HTTPAddr is ":0" in tests).
	ln, err := net.Listen("tcp", o.Config.HTTPAddr)
	if err != nil {
		if vmPool != nil {
			_ = vmPool.Close()
		}
		return nil, fmt.Errorf("dredd: listen %s: %w", o.Config.HTTPAddr, err)
	}
	httpSrv := &http.Server{
		Handler:           srv.Router(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	return &App{
		opts:     o,
		queue:    q,
		pool:     vmPool,
		worker:   w,
		httpSrv:  httpSrv,
		listener: ln,
	}, nil
}

// Run starts the worker pool and the HTTP server and blocks until ctx is
// cancelled or the HTTP server returns an unexpected error.
func (a *App) Run(ctx context.Context) error {
	workerDone := make(chan struct{})
	go func() {
		a.worker.Run(ctx)
		close(workerDone)
	}()

	httpErr := make(chan error, 1)
	go func() {
		err := a.httpSrv.Serve(a.listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		httpErr <- err
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-httpErr:
		return err
	}
}

// Shutdown stops the HTTP server (gracefully, with ctx deadline) and tears
// down the VM pool if dredd owned it.
func (a *App) Shutdown(ctx context.Context) error {
	_ = a.httpSrv.Shutdown(ctx)
	if a.pool != nil {
		_ = a.pool.Close()
	}
	return nil
}

// Addr returns the resolved address the HTTP listener is bound to. Most
// useful when HTTPAddr=":0" so callers can read the assigned port for tests.
func (a *App) Addr() string {
	return a.listener.Addr().String()
}

// Queue exposes the queue for callers that want to inspect or enqueue jobs
// programmatically.
func (a *App) Queue() *queue.Queue { return a.queue }
