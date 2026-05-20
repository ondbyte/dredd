package pool

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/firecracker"
	"github.com/ondbyte/dredd/langs"
)

// onDemandSpare boots a VM per request. In addition it keeps one extra hot
// spare per language so that a failure can be retried instantly without
// paying another cold-boot.
//
// The spares map is set in newOnDemandSpare and never mutated again.
type onDemandSpare struct {
	cfg    *config.Config
	drv    *firecracker.Driver
	reg    *langs.Registry
	spares map[string]chan *firecracker.VM
	sem    bootSemaphore
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed atomic.Bool
}

func newOnDemandSpare(cfg *config.Config, drv *firecracker.Driver, reg *langs.Registry) (*onDemandSpare, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &onDemandSpare{
		cfg:    cfg,
		drv:    drv,
		reg:    reg,
		spares: make(map[string]chan *firecracker.VM),
		sem:    newBootSemaphore(cfg.BootConcurrency),
		ctx:    ctx,
		cancel: cancel,
	}
	for _, l := range reg.All() {
		p.spares[l.ID] = make(chan *firecracker.VM, 1)
		p.wg.Add(1)
		go p.refillSpare(l)
	}
	return p, nil
}

func (p *onDemandSpare) bootFor(ctx context.Context, l langs.Language) (*firecracker.VM, error) {
	if !p.sem.acquire(ctx) {
		return nil, ctx.Err()
	}
	defer p.sem.release()
	dir, err := vmWorkDir(l.ID)
	if err != nil {
		return nil, err
	}
	return p.drv.Boot(ctx, firecracker.BootOptions{
		FirecrackerBin: p.cfg.FirecrackerBin,
		KernelPath:     p.cfg.KernelPath,
		RootfsPath:     l.Rootfs,
		WorkDir:        dir,
		Vcpus:          p.cfg.VMVcpus,
		MemMB:          p.cfg.VMMemMB,
		LanguageID:     l.ID,
	})
}

func (p *onDemandSpare) refillSpare(l langs.Language) {
	defer p.wg.Done()
	vm, err := p.bootFor(p.ctx, l)
	if err != nil {
		if p.ctx.Err() == nil {
			log.Printf("pool ondemand_spare: spare boot %s failed: %v", l.ID, err)
		}
		return
	}
	select {
	case p.spares[l.ID] <- vm:
	case <-p.ctx.Done():
		vm.Kill()
	}
}

func (p *onDemandSpare) Acquire(ctx context.Context, langID string) (*firecracker.VM, error) {
	if p.closed.Load() {
		return nil, errPoolClosed
	}
	l, ok := p.reg.Get(langID)
	if !ok {
		return nil, fmt.Errorf("unknown language %q", langID)
	}
	// Prefer the spare if one is sitting there — that is the "retry buddy".
	if ch, hasCh := p.spares[langID]; hasCh {
		select {
		case vm := <-ch:
			if !p.closed.Load() {
				p.wg.Add(1)
				go p.refillSpare(l)
			}
			return vm, nil
		default:
		}
	}
	return p.bootFor(ctx, l)
}

func (p *onDemandSpare) Release(vm *firecracker.VM) { vm.Kill() }
func (p *onDemandSpare) Discard(vm *firecracker.VM) { vm.Kill() }

// Close stops accepting new Acquires, waits for in-flight refills to settle,
// then drains queued spares. Order matches prewarmedPerLang.Close.
func (p *onDemandSpare) Close() error {
	p.closed.Store(true)
	p.cancel()
	p.wg.Wait()
	for _, ch := range p.spares {
		close(ch)
		for vm := range ch {
			vm.Kill()
		}
	}
	return nil
}
