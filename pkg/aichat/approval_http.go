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
	if err := s.options.Authority.ResolveToolApproval(request.Context(), ToolApprovalResolution{
		ThreadID: threadID, ToolCallID: request.PathValue("toolCallID"),
		Approved: *body.Approved, UpdatedInput: body.UpdatedInput, Reason: body.Reason,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
