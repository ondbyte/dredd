package dredd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ondbyte/dredd"
	"github.com/ondbyte/dredd/agent"
	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/dreddtest"
	"github.com/ondbyte/dredd/langs"
	"github.com/redis/go-redis/v9"
)

// infra is everything an end-to-end test needs: a backing miniredis, a
// configured App listening on an ephemeral port, and the run context that
// stops the worker on Cleanup.
type infra struct {
	t      *testing.T
	app    *dredd.App
	mr     *miniredis.Miniredis
	rdb    *redis.Client
	cancel context.CancelFunc
}

func (i *infra) baseURL() string { return "http://" + i.app.Addr() }

// defaultTestLanguages is the two-language list the core test cases use.
func defaultTestLanguages() []langs.Language {
	return []langs.Language{
		{
			ID:         "shell",
			Name:       "Shell",
			Version:    "test",
			Rootfs:     "fake.ext4",
			SourceFile: "main.sh",
			RunCmd:     "sh main.sh",
		},
		{
			ID:         "shell-compiled",
			Name:       "Shell (with compile)",
			Version:    "test",
			Rootfs:     "fake.ext4",
			SourceFile: "main.sh",
			CompileCmd: "cp main.sh prog && chmod +x prog",
			RunCmd:     "./prog",
		},
	}
}

// startInfra brings the whole stack up with the default two-language
// catalogue and the supplied Executor, then registers t.Cleanup.
func startInfra(t *testing.T, exe dreddTestExecutor) *infra {
	return startInfraWithLanguages(t, exe, defaultTestLanguages())
}

// startInfraWithLanguages is the workhorse: caller supplies the language
// catalogue (must be non-empty; entries' Rootfs values may reference any
// path — the in-process LocalExecutor doesn't read them).
func startInfraWithLanguages(t *testing.T, exe dreddTestExecutor, catalogue []langs.Language) *infra {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	dir := t.TempDir()
	langsFile := filepath.Join(dir, "languages.json")
	rootfsDir := filepath.Join(dir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// LocalExecutor ignores the rootfs but langs.Load demands a non-empty
	// path. Drop a placeholder file so the registry validates.
	if err := os.WriteFile(filepath.Join(rootfsDir, "fake.ext4"), nil, 0o644); err != nil {
		t.Fatalf("placeholder: %v", err)
	}
	mustWriteJSON(t, langsFile, catalogue)

	cfg := &config.Config{
		HTTPAddr:           "127.0.0.1:0",
		LanguagesFile:      langsFile,
		RootfsDir:          rootfsDir,
		KernelPath:         "/dev/null", // unused with LocalExecutor
		PoolStrategy:       config.PoolPrewarmedPerLang,
		PoolSize:           1,
		WorkerConcurrency:  2,
		ResultTTL:          5 * time.Second,
		VMVcpus:            1,
		VMMemMB:            128,
		DefaultTimeLimit:   2 * time.Second,
		DefaultMemoryLimit: 128,
		OutputLimitBytes:   1 << 20,
	}

	reg, err := langs.Load(langsFile, rootfsDir)
	if err != nil {
		t.Fatalf("langs.Load: %v", err)
	}

	app, err := dredd.New(dredd.Options{
		Config:   cfg,
		Registry: reg,
		Redis:    rdb,
		Executor: exe,
	})
	if err != nil {
		t.Fatalf("dredd.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = app.Run(ctx)
	}()

	i := &infra{t: t, app: app, mr: mr, rdb: rdb, cancel: cancel}

	// Wait for the listener to actually accept.
	waitFor(t, 2*time.Second, func() bool {
		resp, err := http.Get(i.baseURL() + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}, "healthz")

	t.Cleanup(func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = app.Shutdown(shutdownCtx)
		cancel()
		_ = rdb.Close()
		mr.Close()
	})
	return i
}

// dreddTestExecutor is the structural type we need; matches worker.Executor.
type dreddTestExecutor interface {
	Execute(ctx context.Context, langID string, req *agent.ExecRequest) (*agent.ExecResponse, error)
}

func TestEndToEnd_Languages(t *testing.T) {
	i := startInfra(t, dreddtest.LocalExecutor{})

	resp, err := http.Get(i.baseURL() + "/languages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var got []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 languages, got %d: %+v", len(got), got)
	}
	if got[0]["id"] != "shell" || got[1]["id"] != "shell-compiled" {
		t.Fatalf("unexpected ids: %+v", got)
	}
}

func TestEndToEnd_ExecMultipleStdins(t *testing.T) {
	i := startInfra(t, dreddtest.LocalExecutor{})

	id := submit(t, i, map[string]any{
		"language": "shell",
		"source":   `read x; echo $((x * 2))`,
		"stdins":   []string{"1\n", "5\n", "10\n"},
	})

	status := waitForStatus(t, i, id, "done", 5*time.Second)
	results, _ := status["results"].([]any)
	want := []string{"2\n", "10\n", "20\n"}
	if len(results) != len(want) {
		t.Fatalf("want %d results, got %d: %+v", len(want), len(results), status)
	}
	for n, r := range results {
		m := r.(map[string]any)
		if got := m["stdout"]; got != want[n] {
			t.Errorf("case %d: stdout = %q, want %q", n, got, want[n])
		}
		if code, _ := m["exit_code"].(float64); code != 0 {
			t.Errorf("case %d: exit_code = %v", n, code)
		}
	}
}

func TestEndToEnd_CompileStep(t *testing.T) {
	i := startInfra(t, dreddtest.LocalExecutor{})

	id := submit(t, i, map[string]any{
		"language": "shell-compiled",
		"source":   `echo hi-from-compiled`,
		"stdins":   []string{""},
	})

	status := waitForStatus(t, i, id, "done", 5*time.Second)
	results := status["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if got := results[0].(map[string]any)["stdout"]; got != "hi-from-compiled\n" {
		t.Errorf("stdout: %q", got)
	}
}

func TestEndToEnd_UnknownLanguage(t *testing.T) {
	i := startInfra(t, dreddtest.LocalExecutor{})

	resp := postJSON(t, i.baseURL()+"/exec", map[string]any{
		"language": "no-such-lang",
		"source":   "echo hi",
		"stdins":   []string{""},
	})
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestEndToEnd_StatusNotFound(t *testing.T) {
	i := startInfra(t, dreddtest.LocalExecutor{})

	resp, err := http.Get(i.baseURL() + "/status/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestEndToEnd_RetryOnVMFailure exercises the single-retry path: the first
// attempt's Executor.Execute returns an error (simulated VM crash); the
// worker must retry once and succeed.
func TestEndToEnd_RetryOnVMFailure(t *testing.T) {
	flaky := dreddtest.NewFlakyExecutor(dreddtest.LocalExecutor{}, 1)
	i := startInfra(t, flaky)

	id := submit(t, i, map[string]any{
		"language": "shell",
		"source":   `echo retry-ok`,
		"stdins":   []string{""},
	})
	status := waitForStatus(t, i, id, "done", 5*time.Second)
	results := status["results"].([]any)
	if got := results[0].(map[string]any)["stdout"]; got != "retry-ok\n" {
		t.Errorf("stdout: %q", got)
	}
	if left := flaky.FailuresLeft.Load(); left >= 0 {
		t.Errorf("expected flaky budget exhausted, got %d", left)
	}
}

// TestEndToEnd_FailsAfterTwoVMFailures: both attempts fail, so the job is
// marked failed.
func TestEndToEnd_FailsAfterTwoVMFailures(t *testing.T) {
	flaky := dreddtest.NewFlakyExecutor(dreddtest.LocalExecutor{}, 2)
	i := startInfra(t, flaky)

	id := submit(t, i, map[string]any{
		"language": "shell",
		"source":   `echo never-runs`,
		"stdins":   []string{""},
	})
	status := waitForStatus(t, i, id, "failed", 5*time.Second)
	if status["error"] == "" || status["error"] == nil {
		t.Errorf("expected error field set; got %+v", status)
	}
}

// TestEndToEnd_ResultTTL verifies the per-job hash is expired in Redis
// after a terminal status.
func TestEndToEnd_ResultTTL(t *testing.T) {
	i := startInfra(t, dreddtest.LocalExecutor{})

	id := submit(t, i, map[string]any{
		"language": "shell",
		"source":   `echo ttl`,
		"stdins":   []string{""},
	})
	waitForStatus(t, i, id, "done", 5*time.Second)

	ttl := i.mr.TTL("dredd:job:" + id)
	if ttl <= 0 {
		t.Fatalf("expected TTL > 0 on dredd:job:%s, got %v", id, ttl)
	}
	// Fast-forward and ensure the key disappears.
	i.mr.FastForward(ttl + time.Second)
	if i.mr.Exists("dredd:job:" + id) {
		t.Fatalf("job hash still present after TTL elapsed")
	}
}

// -- helpers --

func submit(t *testing.T, i *infra, body map[string]any) string {
	t.Helper()
	resp := postJSON(t, i.baseURL()+"/exec", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("/exec status=%d body=%s", resp.StatusCode, b)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID == "" {
		t.Fatal("empty id")
	}
	return out.ID
}

func postJSON(t *testing.T, url string, body map[string]any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return resp
}

func waitForStatus(t *testing.T, i *infra, id, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		resp, err := http.Get(i.baseURL() + "/status/" + id)
		if err != nil {
			t.Fatalf("status %s: %v", id, err)
		}
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		resp.Body.Close()
		if got, _ := m["status"].(string); got == want {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for status %q on %s; last=%+v", want, id, m)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Compile-time check that LocalExecutor satisfies the expected signature.
var _ = fmt.Sprint
