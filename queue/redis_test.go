package queue

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ondbyte/dredd/jobs"
	"github.com/redis/go-redis/v9"
)

func newTestQueue(t *testing.T) (*Queue, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
		mr.Close()
	})
	return New(rdb, 5*time.Second), mr
}

func TestQueue_EnqueueLoadRoundTrip(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()

	job := &jobs.Job{
		ID:            "job-1",
		Language:      "shell",
		Source:        "echo hi",
		Stdins:        []string{"a\n", "b\n"},
		TimeLimitMs:   1500,
		MemoryLimitMb: 64,
		Status:        jobs.StatusQueued,
	}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	got, err := q.Load(ctx, job.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Language != "shell" || got.Source != "echo hi" || len(got.Stdins) != 2 ||
		got.Stdins[1] != "b\n" || got.TimeLimitMs != 1500 || got.MemoryLimitMb != 64 {
		t.Fatalf("unexpected job: %+v", got)
	}
}

func TestQueue_LoadMissing(t *testing.T) {
	q, _ := newTestQueue(t)
	_, err := q.Load(context.Background(), "no-such-job")
	if err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestQueue_LoadCorruptStdinsErrors(t *testing.T) {
	q, mr := newTestQueue(t)
	// Synthesize a corrupted job hash directly via miniredis.
	mr.HSet("dredd:job:bad-1",
		"id", "bad-1",
		"language", "shell",
		"stdins", "not-json",
	)
	_, err := q.Load(context.Background(), "bad-1")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "stdins") {
		t.Fatalf("expected error to mention 'stdins', got %v", err)
	}
}

func TestQueue_LoadCorruptResultsErrors(t *testing.T) {
	q, mr := newTestQueue(t)
	mr.HSet("dredd:job:bad-2",
		"id", "bad-2",
		"language", "shell",
		"results", "<not json>",
	)
	_, err := q.Load(context.Background(), "bad-2")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "results") {
		t.Fatalf("expected error to mention 'results', got %v", err)
	}
}

func TestQueue_LoadCorruptTimeLimitErrors(t *testing.T) {
	q, mr := newTestQueue(t)
	mr.HSet("dredd:job:bad-3",
		"id", "bad-3",
		"language", "shell",
		"time_limit_ms", "not-a-number",
	)
	_, err := q.Load(context.Background(), "bad-3")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "time_limit_ms") {
		t.Fatalf("expected error to mention 'time_limit_ms', got %v", err)
	}
}

func TestQueue_FinishSetsTTL(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()

	job := &jobs.Job{ID: "t1", Language: "shell", Source: "x", Stdins: []string{""}, Status: jobs.StatusQueued}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := q.Finish(ctx, job.ID, jobs.StatusDone, "", "", nil); err != nil {
		t.Fatal(err)
	}
	ttl := mr.TTL("dredd:job:" + job.ID)
	if ttl <= 0 {
		t.Fatalf("expected TTL > 0, got %v", ttl)
	}
}

func TestQueue_FinishFailedAlsoTTLs(t *testing.T) {
	q, mr := newTestQueue(t)
	ctx := context.Background()
	job := &jobs.Job{ID: "t2", Language: "shell", Source: "x", Stdins: []string{""}, Status: jobs.StatusQueued}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := q.Finish(ctx, job.ID, jobs.StatusFailed, "", "boom", nil); err != nil {
		t.Fatal(err)
	}
	if ttl := mr.TTL("dredd:job:" + job.ID); ttl <= 0 {
		t.Fatalf("expected TTL > 0 on failed job, got %v", ttl)
	}
}
