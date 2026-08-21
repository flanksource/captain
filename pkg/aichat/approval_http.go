package aichat

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

func (s *Service) handleResolveToolApproval(w http.ResponseWriter, request *http.Request) {
	store := s.threadStore(w, request)
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
	activeTurnID, err := s.reserveThreadForApproval(threadID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	reserved := true
	defer func() {
		if reserved {
			s.releaseThreadReservation(threadID)
		}
	}()
	continuation, err := s.options.Authority.ResolveToolApproval(request.Context(), ToolApprovalResolution{
		ThreadID: threadID, ApprovalID: request.PathValue("approvalID"),
		ExpectedTurnID: activeTurnID,
		Approved:       *body.Approved, UpdatedInput: body.UpdatedInput, Reason: body.Reason,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if continuation != nil {
		activated, err := s.resumeToolApproval(request.Context(), threadID, continuation)
		if activated {
			reserved = false
		}
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, errThreadBusy) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
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
