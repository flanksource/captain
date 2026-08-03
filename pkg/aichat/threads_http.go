package aichat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func (s *Service) registerThreadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/chat/sessions", s.handleCreateThread)
	mux.HandleFunc("GET /api/chat/sessions", s.handleListThreads)
	mux.HandleFunc("GET /api/chat/sessions/{id}", s.handleGetThread)
	mux.HandleFunc("DELETE /api/chat/sessions/{id}", s.handleDeleteThread)
	mux.HandleFunc("POST /api/chat/sessions/{id}/approvals/{approvalID}", s.handleResolveToolApproval)
	mux.HandleFunc("POST /api/chat/sessions/{id}/interrupt", s.handleInterrupt)
}

func (s *Service) threadStore(w http.ResponseWriter) ThreadStore {
	if s.options.Threads == nil {
		http.Error(w, "thread persistence is not configured", http.StatusNotImplemented)
		return nil
	}
	return s.options.Threads
}

func (s *Service) handleCreateThread(w http.ResponseWriter, request *http.Request) {
	store := s.threadStore(w)
	if store == nil {
		return
	}
	body := struct {
		Title string `json:"title"`
	}{}
	if request.Body != nil {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil && err != io.EOF {
			http.Error(w, fmt.Sprintf("invalid thread request: %v", err), http.StatusBadRequest)
			return
		}
	}
	if body.Title == "" {
		body.Title = "New conversation"
	}
	thread, err := store.Create(request.Context(), body.Title)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeJSON(w, http.StatusCreated, thread); err != nil {
		serviceLog.Errorf("write created chat thread: %v", err)
	}
}

func (s *Service) handleListThreads(w http.ResponseWriter, request *http.Request) {
	store := s.threadStore(w)
	if store == nil {
		return
	}
	threads, err := store.List(request.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeJSON(w, http.StatusOK, threads); err != nil {
		serviceLog.Errorf("write chat thread list: %v", err)
	}
}

func (s *Service) handleGetThread(w http.ResponseWriter, request *http.Request) {
	store := s.threadStore(w)
	if store == nil {
		return
	}
	if sessions, ok := store.(SessionReader); ok {
		aggregate, err := sessions.GetSession(request.Context(), request.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := writeJSON(w, http.StatusOK, aggregate); err != nil {
			serviceLog.Errorf("write chat session %q: %v", request.PathValue("id"), err)
		}
		return
	}
	thread, err := store.Get(request.Context(), request.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err := writeJSON(w, http.StatusOK, thread); err != nil {
		serviceLog.Errorf("write chat thread %q: %v", request.PathValue("id"), err)
	}
}

func (s *Service) handleDeleteThread(w http.ResponseWriter, request *http.Request) {
	store := s.threadStore(w)
	if store == nil {
		return
	}
	if err := store.Delete(request.Context(), request.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write JSON response: %w", err)
	}
	return nil
}
