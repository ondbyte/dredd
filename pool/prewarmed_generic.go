package pool

import (
	"context"
	"log"
	"sync"

	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/firecracker"
	"github.com/ondbyte/dredd/langs"
)

// prewarmedGeneric keeps N generic VMs booted off a single rootfs that
// carries every supported toolchain (or none — the guest agent shells out
// to whatever RunCmd specifies). The exec protocol already carries the
// language-specific commands, so the VM does not need to be specialised.
type prewarmedGeneric struct {
	cfg    *config.Config
	drv    *firecracker.Driver
	size   int
	pool   chan *firecracker.VM
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newPrewarmedGeneric(cfg *config.Config, drv *firecracker.Driver, _ *langs.Registry) (*prewarmedGeneric, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &prewarmedGeneric{
		cfg:    cfg,
		drv:    drv,
		size:   cfg.PoolSize,
		pool:   make(chan *firecracker.VM, cfg.PoolSize),
		ctx:    ctx,
		cancel: cancel,
	}
	for i := 0; i < p.size; i++ {
		p.wg.Add(1)
		go p.refill()
	}
	return p, nil
}

func (p *prewarmedGeneric) refill() {
	defer p.wg.Done()
	vm, err := p.boot()
	if err != nil {
		log.Printf("pool prewarmed_generic: boot failed: %v", err)
		return
	}
	select {
	case p.pool <- vm:
	case <-p.ctx.Done():
		vm.Kill()
	}
}

func (p *prewarmedGeneric) boot() (*firecracker.VM, error) {
	dir, err := vmWorkDir("generic")
	if err != nil {
		return nil, err
	}
	return p.drv.Boot(p.ctx, firecracker.BootOptions{
		FirecrackerBin: p.cfg.FirecrackerBin,
		KernelPath:     p.cfg.KernelPath,
		RootfsPath:     p.cfg.GenericRootfs,
		WorkDir:        dir,
		Vcpus:          p.cfg.VMVcpus,
		MemMB:          p.cfg.VMMemMB,
	})
}

func (p *prewarmedGeneric) Acquire(ctx context.Context, _ string) (*firecracker.VM, error) {
	select {
	case vm := <-p.pool:
		p.wg.Add(1)
		go p.refill()
		return vm, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *prewarmedGeneric) Release(vm *firecracker.VM) { vm.Kill() }
func (p *prewarmedGeneric) Discard(vm *firecracker.VM) { vm.Kill() }

func (p *prewarmedGeneric) Close() error {
	p.cancel()
	close(p.pool)
	for vm := range p.pool {
		vm.Kill()
	}
	p.wg.Wait()
	return nil
}
