package jobs

import "github.com/ondbyte/dredd/agent"

// Job is the in-memory representation of a submission. Redis stores the same
// fields as a hash keyed by JobID; see the queue package.
type Job struct {
	ID            string             `json:"id"`
	Language      string             `json:"language"`
	Source        string             `json:"source"`
	Stdins        []string           `json:"stdins"`
	TimeLimitMs   int                `json:"time_limit_ms"`
	MemoryLimitMb int                `json:"memory_limit_mb"`
	Status        Status             `json:"status"`
	Error         string             `json:"error,omitempty"`
	CompileError  string             `json:"compile_error,omitempty"`
	Results       []agent.CaseResult `json:"results,omitempty"`
}
