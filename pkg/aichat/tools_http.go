package aichat

import (
	"net/http"

	aitools "github.com/flanksource/captain/pkg/ai/tools"
)

func (s *Service) registerToolRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chat/tools", s.handleTools)
}

func (s *Service) handleTools(w http.ResponseWriter, request *http.Request) {
	set, err := s.loadTools(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeJSON(w, http.StatusOK, aitools.ToolCatalog{Tools: set.Catalog}); err != nil {
		serviceLog.Errorf("write chat tools response: %v", err)
	}
}
