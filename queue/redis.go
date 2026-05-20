package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ondbyte/dredd/agent"
	"github.com/ondbyte/dredd/jobs"
	"github.com/redis/go-redis/v9"
)

const (
	queueKey  = "dredd:queue"
	jobKeyFmt = "dredd:job:%s"
	// popTimeout bounds a single BRPop. The worker simply re-issues it on
	// timeout, so this is a tradeoff between idle Redis traffic and shutdown
	// latency — workers must finish their in-flight BRPop before they notice
	// a cancelled context.
	popTimeout = 500 * time.Millisecond
)

// ErrNotFound is returned when no hash exists for a given job ID.
var ErrNotFound = errors.New("job not found")

type Queue struct {
	rdb       *redis.Client
	resultTTL time.Duration
}

func New(rdb *redis.Client, resultTTL time.Duration) *Queue {
	return &Queue{rdb: rdb, resultTTL: resultTTL}
}

func jobKey(id string) string { return fmt.Sprintf(jobKeyFmt, id) }

// Enqueue writes the job hash and pushes its ID onto the work list.
func (q *Queue) Enqueue(ctx context.Context, j *jobs.Job) error {
	stdinsJSON, err := json.Marshal(j.Stdins)
	if err != nil {
		return err
	}
	key := jobKey(j.ID)
	pipe := q.rdb.TxPipeline()
	pipe.HSet(ctx, key, map[string]any{
		"id":              j.ID,
		"language":        j.Language,
		"source":          j.Source,
		"stdins":          string(stdinsJSON),
		"time_limit_ms":   j.TimeLimitMs,
		"memory_limit_mb": j.MemoryLimitMb,
		"status":          string(jobs.StatusQueued),
	})
	pipe.LPush(ctx, queueKey, j.ID)
	_, err = pipe.Exec(ctx)
	return err
}

// Dequeue blocks until a job ID is available or the context is cancelled.
// Returns "" on timeout or context cancel without error so the caller can
// loop cleanly.
func (q *Queue) Dequeue(ctx context.Context) (string, error) {
	res, err := q.rdb.BRPop(ctx, popTimeout, queueKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return "", nil
		}
		return "", err
	}
	if len(res) < 2 {
		return "", nil
	}
	return res[1], nil
}

// Load reads a job hash. Returns ErrNotFound if the key is missing.
func (q *Queue) Load(ctx context.Context, id string) (*jobs.Job, error) {
	m, err := q.rdb.HGetAll(ctx, jobKey(id)).Result()
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, ErrNotFound
	}
	j := &jobs.Job{
		ID:           m["id"],
		Language:     m["language"],
		Source:       m["source"],
		Status:       jobs.Status(m["status"]),
		Error:        m["error"],
		CompileError: m["compile_error"],
	}
	if s := m["stdins"]; s != "" {
		_ = json.Unmarshal([]byte(s), &j.Stdins)
	}
	if s := m["results"]; s != "" {
		_ = json.Unmarshal([]byte(s), &j.Results)
	}
	if s := m["time_limit_ms"]; s != "" {
		fmt.Sscanf(s, "%d", &j.TimeLimitMs)
	}
	if s := m["memory_limit_mb"]; s != "" {
		fmt.Sscanf(s, "%d", &j.MemoryLimitMb)
	}
	return j, nil
}

// SetRunning marks a job as in-progress.
func (q *Queue) SetRunning(ctx context.Context, id string) error {
	return q.rdb.HSet(ctx, jobKey(id), "status", string(jobs.StatusRunning)).Err()
}

// Finish writes the final state. If the status is terminal, the job hash
// gets a TTL so it is purged automatically.
func (q *Queue) Finish(ctx context.Context, id string, status jobs.Status, compileErr, errStr string, results []agent.CaseResult) error {
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return err
	}
	pipe := q.rdb.TxPipeline()
	pipe.HSet(ctx, jobKey(id), map[string]any{
		"status":        string(status),
		"compile_error": compileErr,
		"error":         errStr,
		"results":       string(resultsJSON),
	})
	if status.Terminal() && q.resultTTL > 0 {
		pipe.Expire(ctx, jobKey(id), q.resultTTL)
	}
	_, err = pipe.Exec(ctx)
	return err
}
