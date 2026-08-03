package aichat

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (s *Service) handleResolveToolApproval(w http.ResponseWriter, request *http.Request) {
	store := s.threadStore(w)
	if store == nil {
		return
	}
	if s.options.Authority == nil {
		http.Error(w, "execution authority is not configured", http.StatusNotImplemented)
		return
	}
	threadID := request.PathValue("id")
	if _, err := store.Get(request.Context(), threadID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	body := struct {
		Approved     *bool          `json:"approved"`
		UpdatedInput map[string]any `json:"updatedInput,omitempty"`
		Reason       string         `json:"reason,omitempty"`
	}{}
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, fmt.Sprintf("invalid tool approval decision: %v", err), http.StatusBadRequest)
		return
	}
	if body.Approved == nil {
		http.Error(w, "tool approval decision requires approved", http.StatusBadRequest)
		return
	}
	if !*body.Approved && body.UpdatedInput != nil {
		http.Error(w, "denied tool approval cannot replace input", http.StatusBadRequest)
		return
	}
	continuation, err := s.options.Authority.ResolveToolApproval(request.Context(), ToolApprovalResolution{
		ThreadID: threadID, ApprovalID: request.PathValue("approvalID"),
		Approved: *body.Approved, UpdatedInput: body.UpdatedInput, Reason: body.Reason,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if continuation != nil {
		if err := s.resumeToolApproval(request.Context(), threadID, continuation); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	if sessions, ok := store.(SessionReader); ok {
		aggregate, err := sessions.GetSession(request.Context(), threadID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := writeJSON(w, http.StatusOK, aggregate); err != nil {
			serviceLog.Errorf("write approved chat session %q: %v", threadID, err)
		}
		return
	}
	thread, err := store.Get(request.Context(), threadID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeJSON(w, http.StatusOK, thread); err != nil {
		serviceLog.Errorf("write approved chat thread %q: %v", threadID, err)
	}
}
