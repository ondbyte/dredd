package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ondbyte/dredd/agent"
	"github.com/ondbyte/dredd/jobs"
	"github.com/ondbyte/dredd/queue"
)

type statusResponse struct {
	ID           string             `json:"id"`
	Language     string             `json:"language"`
	Status       jobs.Status        `json:"status"`
	CompileError string             `json:"compile_error,omitempty"`
	Error        string             `json:"error,omitempty"`
	Results      []agent.CaseResult `json:"results"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	job, err := s.q.Load(r.Context(), id)
	if err != nil {
		if errors.Is(err, queue.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "job not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := statusResponse{
		ID:           job.ID,
		Language:     job.Language,
		Status:       job.Status,
		CompileError: job.CompileError,
		Error:        job.Error,
		Results:      job.Results,
	}
	if resp.Results == nil {
		resp.Results = []agent.CaseResult{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
