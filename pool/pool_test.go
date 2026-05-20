package pool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBootSemaphore_CapsConcurrency(t *testing.T) {
	const cap, workers = 3, 20
	sem := newBootSemaphore(cap)

	var (
		inFlight    atomic.Int32
		peak        atomic.Int32
		completions atomic.Int32
		wg          sync.WaitGroup
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !sem.acquire(ctx) {
				return
			}
			defer sem.release()

			cur := inFlight.Add(1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			// Hold the slot briefly so others queue behind us.
			time.Sleep(20 * time.Millisecond)
			inFlight.Add(-1)
			completions.Add(1)
		}()
	}
	wg.Wait()
	if got := completions.Load(); int(got) != workers {
		t.Fatalf("completions = %d, want %d", got, workers)
	}
	if got := peak.Load(); int(got) > cap {
		t.Fatalf("peak concurrency = %d, exceeds cap %d", got, cap)
	}
}

func TestBootSemaphore_AcquireRespectsContext(t *testing.T) {
	sem := newBootSemaphore(1)
	// Fill the one slot and never release.
	if !sem.acquire(context.Background()) {
		t.Fatal("first acquire should succeed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := sem.acquire(ctx)
	if got {
		t.Fatal("acquire should have failed under saturated semaphore + cancelled ctx")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("acquire took too long to give up: %v", d)
	}
}

func TestBootSemaphore_FloorsToOne(t *testing.T) {
	sem := newBootSemaphore(0)
	if !sem.acquire(context.Background()) {
		t.Fatal("acquire on floored-to-1 semaphore should succeed")
	}
	sem.release()
	// A second acquire should also succeed once released.
	if !sem.acquire(context.Background()) {
		t.Fatal("second acquire should succeed")
	}
	sem.release()
}
