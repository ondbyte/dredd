package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/langs"
	"github.com/ondbyte/dredd/queue"
	"github.com/redis/go-redis/v9"
)

// newTestServer wires the API layer with miniredis-backed storage and a
// single-language registry. Returned cleanup must be called by the test.
func newTestServer(t *testing.T, maxBytes int64) (*Server, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	dir := t.TempDir()
	langsPath := filepath.Join(dir, "languages.json")
	if err := os.WriteFile(langsPath, []byte(`[{"id":"shell","name":"Shell","rootfs":"fake","source_file":"main.sh","run_cmd":"sh main.sh"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := langs.Load(langsPath, dir)
	if err != nil {
		t.Fatalf("langs.Load: %v", err)
	}

	cfg := &config.Config{
		MaxRequestBytes: maxBytes,
		ResultTTL:       60 * time.Second,
	}
	q := queue.New(rdb, cfg.ResultTTL)
	srv := NewServer(cfg, reg, q)
	return srv, func() { _ = rdb.Close(); mr.Close() }
}

func TestExec_BodySizeLimit(t *testing.T) {
	srv, cleanup := newTestServer(t, 256) // 256-byte cap
	defer cleanup()

	big := strings.Repeat("a", 1024)
	body := `{"language":"shell","source":"` + big + `","stdins":[""]}`
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.exec(rec, req)
	if got := rec.Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (body=%s)", got, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
}

func TestExec_BodyUnderLimitAccepted(t *testing.T) {
	srv, cleanup := newTestServer(t, 1<<20)
	defer cleanup()

	body := `{"language":"shell","source":"echo hi","stdins":[""]}`
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.exec(rec, req)
	if got := rec.Code; got != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body=%s)", got, rec.Body.String())
	}
}

func TestExec_UnknownLanguageRejected(t *testing.T) {
	srv, cleanup := newTestServer(t, 0)
	defer cleanup()

	body := `{"language":"nope","source":"x","stdins":[""]}`
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.exec(rec, req)
	if got := rec.Code; got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}

func TestExec_UnknownFieldsRejected(t *testing.T) {
	srv, cleanup := newTestServer(t, 0)
	defer cleanup()

	body := `{"language":"shell","source":"x","stdins":[""],"surprise":1}`
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.exec(rec, req)
	if got := rec.Code; got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (got body %s)", got, rec.Body.String())
	}
}
