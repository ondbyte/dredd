package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) languages(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.reg.PublicAll())
}
