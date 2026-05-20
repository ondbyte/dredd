// Package agent defines the wire protocol shared between the dredd host
// process and the guest dreddagent running inside each Firecracker MicroVM.
//
// The host sends exactly one ExecRequest over a vsock connection. The guest
// answers with exactly one ExecResponse and then powers the VM off.
//
// Framing is length-prefixed JSON: a 4-byte big-endian uint32 payload length,
// followed by that many bytes of JSON.
package agent

// VsockPort is the vsock port the guest agent listens on.
const VsockPort uint32 = 52000

// MaxFrameBytes caps a single framed message. Anything larger is a protocol
// error and the connection is closed.
const MaxFrameBytes = 32 * 1024 * 1024

// ExecRequest is the single message the host sends after the VM boots.
type ExecRequest struct {
	// SourceFile is the file name the source is written to in /work, e.g.
	// "main.py" or "main.c".
	SourceFile string `json:"source_file"`
	// Source is the program source code.
	Source string `json:"source"`
	// CompileCmd, if non-empty, is run once before any test case. A non-zero
	// exit code aborts the request and is surfaced via ExecResponse.CompileError.
	CompileCmd string `json:"compile_cmd"`
	// RunCmd is executed once per stdin. It is passed through /bin/sh -c.
	RunCmd string `json:"run_cmd"`
	// Stdins is the ordered list of stdin payloads to feed RunCmd.
	Stdins []string `json:"stdins"`
	// TimeLimitMs is a per-test-case wall-clock limit.
	TimeLimitMs int `json:"time_limit_ms"`
	// MemoryLimitMb is a per-test-case memory cap (RSS) enforced via cgroups.
	MemoryLimitMb int `json:"memory_limit_mb"`
	// OutputLimitBytes truncates stdout/stderr captured per case.
	OutputLimitBytes int `json:"output_limit_bytes"`
}

// CaseResult is the outcome of running RunCmd against one stdin entry.
type CaseResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimeMs   int    `json:"time_ms"`
	MemoryKb int    `json:"memory_kb"`
	TimedOut bool   `json:"timed_out"`
}

// ExecResponse is the single reply the guest sends back. Exactly one of
// CompileError or Results is populated on success; AgentError is set if the
// guest itself failed before it could run user code.
type ExecResponse struct {
	CompileError string       `json:"compile_error,omitempty"`
	Results      []CaseResult `json:"results,omitempty"`
	AgentError   string       `json:"agent_error,omitempty"`
}
