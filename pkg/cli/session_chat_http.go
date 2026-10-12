package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	sessionquery "github.com/flanksource/captain/pkg/session/query"
	"github.com/google/uuid"
)

// SessionChat is the live chat run attached to a session in this process: the
// run, what it can do now, its state frame, and its permission posture — the
// current one and those its runtime honours.
type SessionChat struct {
	RunID           string               `json:"runId"`
	Capabilities    ChatCapabilities     `json:"capabilities"`
	State           ChatStateFrame       `json:"state"`
	PermissionMode  api.PermissionMode   `json:"permissionMode,omitempty"`
	PermissionModes []api.PermissionMode `json:"permissionModes,omitempty"`
}

func sessionChatOf(chat *chatSession) SessionChat {
	runID, capabilities, state := chat.projection()
	return SessionChat{
		RunID: runID, Capabilities: capabilities, State: *state,
		PermissionMode: state.PermissionMode, PermissionModes: state.PermissionModes,
	}
}

// handleSessionChat answers the session's live chat, or a 404 when none is
// attached in this process.
func handleSessionChat(chats *chatBroker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chat, err := resolveSessionChat(r, chats, http.StatusNotFound)
		if err != nil {
			writeSessionChatError(w, err)
			return
		}
		writeChatJSON(w, http.StatusOK, sessionChatOf(chat))
	}
}

// handleSessionChatControl runs a chat control on the session's live chat, or
// answers 409 when none is attached in this process.
func handleSessionChatControl(chats *chatBroker, control chatControl) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chat, err := resolveSessionChat(r, chats, http.StatusConflict)
		if err != nil {
			writeSessionChatError(w, err)
			return
		}
		response, err := control(r, chat)
		if err != nil {
			writeSessionChatError(w, err)
			return
		}
		writeChatJSON(w, http.StatusAccepted, response)
	}
}

// resolveSessionChat finds the live chat on the session the path names. A
// provider session id with a live chat answers from the broker alone; any other
// id resolves to its provider session id from the session rows, never reading
// a transcript. absent is the status when the session has no live chat.
func resolveSessionChat(r *http.Request, chats *chatBroker, absent int) (*chatSession, error) {
	id := strings.TrimSpace(r.PathValue("id"))
	if chat, ok := chats.getSession(id); ok {
		return chat, nil
	}
	providerID, err := sessionProviderID(r.Context(), id)
	if err != nil {
		return nil, err
	}
	chat, ok := chats.getSession(providerID)
	if !ok {
		return nil, newChatError(absent, fmt.Sprintf("session %s has no live chat in this process", id))
	}
	return chat, nil
}

// sessionProviderID resolves a captain or provider session id to the provider
// session a live chat binds to, from the folded session rows alone. A root's
// captain id names the root, not its subagents; otherwise, like
// selectResumeSession, rows naming different provider sessions are ambiguous.
func sessionProviderID(ctx context.Context, id string) (string, error) {
	db, err := captainDB(ctx)
	if err != nil {
		return "", err
	}
	sessions, err := sessionquery.ResolveFolded(ctx, db, id)
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", newChatError(http.StatusNotFound, "unknown session")
	}
	rows := make([]sessionRowIdentity, len(sessions))
	for i, folded := range sessions {
		rows[i] = sessionRowIdentity{ID: folded.Session.ID.String()}
		if folded.Session.RootSessionID != nil {
			rows[i].RootID = folded.Session.RootSessionID.String()
		}
	}
	if isThreadExpansion(id, rows) {
		sessions = sessions[:1]
	}
	providerID := strings.TrimSpace(stringOr(sessions[0].Session.ProviderSessionID, ""))
	for _, folded := range sessions[1:] {
		if providerID == "" || strings.TrimSpace(stringOr(folded.Session.ProviderSessionID, "")) != providerID {
			return "", newChatError(http.StatusConflict, "session id is ambiguous")
		}
	}
	return providerID, nil
}

// sessionRowIdentity is the part of a resolved session row that tells a thread
// expansion apart from an ambiguous id.
type sessionRowIdentity struct{ ID, RootID string }

// isThreadExpansion reports whether rows are how sessionquery.Resolve answers a
// root session's exact captain id: the root first, then its whole thread, every
// other row naming that root. The id then names the root alone; the subagent
// rows carry their own provider session ids and must not read as ambiguity.
func isThreadExpansion(id string, rows []sessionRowIdentity) bool {
	root, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil || len(rows) < 2 || rows[0].ID != root.String() || rows[0].RootID != "" {
		return false
	}
	for _, row := range rows[1:] {
		if row.RootID != root.String() {
			return false
		}
	}
	return true
}

// writeSessionChatError answers a JSON {error} with the status a chat error
// names, or the one a session lookup error maps to.
func writeSessionChatError(w http.ResponseWriter, err error) {
	status := sessionErrorStatus(err)
	var target chatHTTPError
	if errors.As(err, &target) {
		status = target.status
	}
	writeSessionError(w, status, err.Error(), nil)
}
