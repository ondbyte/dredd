// Package pool manages a set of running Firecracker MicroVMs and hands them
// out to the worker. Three strategies are provided behind one interface; the
// caller selects via DREDD_POOL_STRATEGY.
package pool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/firecracker"
	"github.com/ondbyte/dredd/langs"
)

// VMPool is the interface that the worker depends on. All implementations
// must be safe for concurrent use.
type VMPool interface {
	// Acquire returns a booted VM ready to serve a request for langID.
	Acquire(ctx context.Context, langID string) (*firecracker.VM, error)
	// Release returns a VM to the pool — but VMs in dredd are single-use
	// (the guest reboots after each request), so most implementations will
	// simply discard. The interface accommodates future reuse.
	Release(vm *firecracker.VM)
	// Discard kills a VM the worker has decided is unhealthy and refills if
	// the strategy maintains a warm pool.
	Discard(vm *firecracker.VM)
	// Close shuts down the pool and kills any idle VMs.
	Close() error
}

// New returns the implementation selected by cfg.PoolStrategy.
func New(cfg *config.Config, drv *firecracker.Driver, reg *langs.Registry) (VMPool, error) {
	switch cfg.PoolStrategy {
	case config.PoolPrewarmedPerLang:
		return newPrewarmedPerLang(cfg, drv, reg)
	case config.PoolOnDemandSpare:
		return newOnDemandSpare(cfg, drv, reg)
	case config.PoolPrewarmedGeneric:
		return newPrewarmedGeneric(cfg, drv, reg)
	default:
		return nil, fmt.Errorf("unknown pool strategy %q", cfg.PoolStrategy)
	}
}

// vmWorkDir returns a unique scratch directory for a VM's sockets and other
// per-VM state.
func vmWorkDir(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", "dredd-"+prefix+"-")
	if err != nil {
		return "", err
	}
	return filepath.Clean(dir), nil
}

