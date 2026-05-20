package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ondbyte/dredd/config"
	"github.com/ondbyte/dredd/langs"
	"github.com/ondbyte/dredd/queue"
)

type Server struct {
	cfg *config.Config
	reg *langs.Registry
	q   *queue.Queue
}

func NewServer(cfg *config.Config, reg *langs.Registry, q *queue.Queue) *Server {
	return &Server{cfg: cfg, reg: reg, q: q}
}

func (s *Server) Router() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Logger)
	r.Get("/healthz", s.health)
	r.Get("/languages", s.languages)
	r.Post("/exec", s.exec)
	r.Get("/status/{id}", s.status)
	return r
}
