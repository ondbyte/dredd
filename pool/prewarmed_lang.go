package pool

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/firecracker"
	"github.com/ondbyte/dredd/langs"
)

// prewarmedPerLang keeps a channel of N booted VMs per language. Each VM in
// dredd is single-use (the guest reboots after handling one request), so
// Release simply discards and the refill goroutine boots a replacement.
type prewarmedPerLang struct {
	cfg     *config.Config
	drv     *firecracker.Driver
	reg     *langs.Registry
	size    int
	pools   map[string]chan *firecracker.VM
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func newPrewarmedPerLang(cfg *config.Config, drv *firecracker.Driver, reg *langs.Registry) (*prewarmedPerLang, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &prewarmedPerLang{
		cfg:    cfg,
		drv:    drv,
		reg:    reg,
		size:   cfg.PoolSize,
		pools:  make(map[string]chan *firecracker.VM),
		ctx:    ctx,
		cancel: cancel,
	}
	for _, l := range reg.All() {
		p.pools[l.ID] = make(chan *firecracker.VM, p.size)
		for i := 0; i < p.size; i++ {
			p.wg.Add(1)
			go p.refill(l)
		}
	}
	return p, nil
}

func (p *prewarmedPerLang) refill(l langs.Language) {
	defer p.wg.Done()
	vm, err := p.bootFor(l)
	if err != nil {
		log.Printf("pool prewarmed_per_lang: boot %s failed: %v", l.ID, err)
		return
	}
	select {
	case p.pools[l.ID] <- vm:
	case <-p.ctx.Done():
		vm.Kill()
	}
}

func (p *prewarmedPerLang) bootFor(l langs.Language) (*firecracker.VM, error) {
	dir, err := vmWorkDir(l.ID)
	if err != nil {
		return nil, err
	}
	return p.drv.Boot(p.ctx, firecracker.BootOptions{
		FirecrackerBin: p.cfg.FirecrackerBin,
		KernelPath:     p.cfg.KernelPath,
		RootfsPath:     l.Rootfs,
		WorkDir:        dir,
		Vcpus:          p.cfg.VMVcpus,
		MemMB:          p.cfg.VMMemMB,
		LanguageID:     l.ID,
	})
}

func (p *prewarmedPerLang) Acquire(ctx context.Context, langID string) (*firecracker.VM, error) {
	p.mu.Lock()
	ch, ok := p.pools[langID]
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no pool for language %q", langID)
	}
	select {
	case vm := <-ch:
		// Replace the consumed VM in the background.
		l, _ := p.reg.Get(langID)
		p.wg.Add(1)
		go p.refill(l)
		return vm, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *prewarmedPerLang) Release(vm *firecracker.VM) {
	// Single-use: the guest rebooted after exec, so the VM is dead. Just clean up.
	vm.Kill()
}

func (p *prewarmedPerLang) Discard(vm *firecracker.VM) {
	vm.Kill()
}

func (p *prewarmedPerLang) Close() error {
	p.cancel()
	// Drain pools.
	for _, ch := range p.pools {
		close(ch)
		for vm := range ch {
			vm.Kill()
		}
	}
	p.wg.Wait()
	return nil
}
