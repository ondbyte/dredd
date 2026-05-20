package pool

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/firecracker"
	"github.com/ondbyte/dredd/langs"
)

// errPoolClosed is returned by Acquire after Close has been called.
var errPoolClosed = errors.New("pool: closed")

// prewarmedPerLang keeps a channel of N booted VMs per language. Each VM in
// dredd is single-use (the guest reboots after handling one request), so
// Release simply discards and the refill goroutine boots a replacement.
//
// The pools map is set in newPrewarmedPerLang and never mutated again, so
// reads are safe without a lock.
type prewarmedPerLang struct {
	cfg    *config.Config
	drv    *firecracker.Driver
	reg    *langs.Registry
	size   int
	pools  map[string]chan *firecracker.VM
	sem    bootSemaphore
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed atomic.Bool
}

func newPrewarmedPerLang(cfg *config.Config, drv *firecracker.Driver, reg *langs.Registry) (*prewarmedPerLang, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &prewarmedPerLang{
		cfg:    cfg,
		drv:    drv,
		reg:    reg,
		size:   cfg.PoolSize,
		pools:  make(map[string]chan *firecracker.VM),
		sem:    newBootSemaphore(cfg.BootConcurrency),
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
	// Throttle parallel boots; abandon if the pool is shutting down.
	if !p.sem.acquire(p.ctx) {
		return
	}
	vm, err := p.bootFor(l)
	p.sem.release()
	if err != nil {
		// Ignore errors caused by the pool shutting down mid-boot.
		if p.ctx.Err() == nil {
			log.Printf("pool prewarmed_per_lang: boot %s failed: %v", l.ID, err)
		}
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
	if p.closed.Load() {
		return nil, errPoolClosed
	}
	ch, ok := p.pools[langID]
	if !ok {
		return nil, fmt.Errorf("no pool for language %q", langID)
	}
	select {
	case vm := <-ch:
		// Replace the consumed VM in the background — unless we're shutting down.
		if !p.closed.Load() {
			l, _ := p.reg.Get(langID)
			p.wg.Add(1)
			go p.refill(l)
		}
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

// Close stops accepting new Acquires, waits for any in-flight refills to
// settle (they'll see ctx.Done and abandon or push their VM into the pool),
// then drains and kills every queued VM. Order matters: closing the pool
// channels before draining the refill goroutines would cause "send on
// closed channel" panics in any refill that wins its select race.
func (p *prewarmedPerLang) Close() error {
	p.closed.Store(true)
	p.cancel()
	p.wg.Wait()
	for _, ch := range p.pools {
		close(ch)
		for vm := range ch {
			vm.Kill()
		}
	}
	return nil
}
