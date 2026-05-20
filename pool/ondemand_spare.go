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

// onDemandSpare boots a VM per request. In addition it keeps one extra hot
// spare per language so that a failure can be retried instantly without
// paying another cold-boot.
type onDemandSpare struct {
	cfg    *config.Config
	drv    *firecracker.Driver
	reg    *langs.Registry
	spares map[string]chan *firecracker.VM
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newOnDemandSpare(cfg *config.Config, drv *firecracker.Driver, reg *langs.Registry) (*onDemandSpare, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &onDemandSpare{
		cfg:    cfg,
		drv:    drv,
		reg:    reg,
		spares: make(map[string]chan *firecracker.VM),
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

func (p *onDemandSpare) bootFor(l langs.Language) (*firecracker.VM, error) {
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

func (p *onDemandSpare) refillSpare(l langs.Language) {
	defer p.wg.Done()
	vm, err := p.bootFor(l)
	if err != nil {
		log.Printf("pool ondemand_spare: spare boot %s failed: %v", l.ID, err)
		return
	}
	select {
	case p.spares[l.ID] <- vm:
	case <-p.ctx.Done():
		vm.Kill()
	}
}

func (p *onDemandSpare) Acquire(ctx context.Context, langID string) (*firecracker.VM, error) {
	l, ok := p.reg.Get(langID)
	if !ok {
		return nil, fmt.Errorf("unknown language %q", langID)
	}
	// Prefer the spare if one is sitting there — that is the "retry buddy".
	p.mu.Lock()
	ch, hasCh := p.spares[langID]
	p.mu.Unlock()
	if hasCh {
		select {
		case vm := <-ch:
			p.wg.Add(1)
			go p.refillSpare(l)
			return vm, nil
		default:
		}
	}
	return p.bootFor(l)
}

func (p *onDemandSpare) Release(vm *firecracker.VM) { vm.Kill() }
func (p *onDemandSpare) Discard(vm *firecracker.VM) { vm.Kill() }

func (p *onDemandSpare) Close() error {
	p.cancel()
	for _, ch := range p.spares {
		close(ch)
		for vm := range ch {
			vm.Kill()
		}
	}
	p.wg.Wait()
	return nil
}
