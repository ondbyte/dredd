package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/ondbyte/dredd/jobs"
)

type execRequest struct {
	Language      string   `json:"language"`
	Source        string   `json:"source"`
	Stdins        []string `json:"stdins"`
	TimeLimitMs   int      `json:"time_limit_ms"`
	MemoryLimitMb int      `json:"memory_limit_mb"`
}

type execResponse struct {
	ID string `json:"id"`
}

func (s *Server) exec(w http.ResponseWriter, r *http.Request) {
	// Cap request body to MaxRequestBytes to keep a malicious client from
	// pushing a multi-GB payload through the JSON decoder. Zero disables
	// the cap (not recommended).
	body := r.Body
	if s.cfg.MaxRequestBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBytes)
	}
	var req execRequest
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", mbe.Limit))
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if err := validate(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := s.reg.Get(req.Language); !ok {
		writeErr(w, http.StatusBadRequest, "unknown language: "+req.Language)
		return
	}
	id, err := ulid.New(ulid.Timestamp(time.Now()), rand.Reader)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "id: "+err.Error())
		return
	}
	job := &jobs.Job{
		ID:            id.String(),
		Language:      req.Language,
		Source:        req.Source,
		Stdins:        req.Stdins,
		TimeLimitMs:   req.TimeLimitMs,
		MemoryLimitMb: req.MemoryLimitMb,
		Status:        jobs.StatusQueued,
	}
	if err := s.q.Enqueue(r.Context(), job); err != nil {
		writeErr(w, http.StatusInternalServerError, "enqueue: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(execResponse{ID: job.ID})
}

func validate(r execRequest) error {
	if r.Language == "" {
		return errors.New("language is required")
	}
	if r.Source == "" {
		return errors.New("source is required")
	}
	if len(r.Stdins) == 0 {
		return errors.New("stdins must contain at least one entry")
	}
	return nil
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
