package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	sessionquery "github.com/flanksource/captain/pkg/session/query"
)

// sessionStreamKeepAlive is how often an idle follow stream sends an SSE
// comment so proxies and browsers keep the connection open.
var sessionStreamKeepAlive = 15 * time.Second

type sessionReadResponse struct {
	*session.Session
	DetailAvailable bool              `json:"detailAvailable"`
	DetailSource    map[string]string `json:"detailSource"`
}

// SessionHandler serves one Captain session by identity, with paths relative to
// wherever a host mounts it (captain serve and gavel mount it at
// /api/captain/sessions via http.StripPrefix):
//
//   - GET /{id} returns the unified session.Session that `captain sessions get`
//     composes, with the whole transcript unless tail, offset or limit narrow it.
//     before=<message id> pages history: the messages ahead of that one (the
//     last `limit` of them), without events.
//   - GET /{id}?follow=1, or with Accept: text/event-stream, follows the session
//     over SSE: `entry` frames carry a session.Message upsert, `state` frames a
//     FollowState, `error` frames a terminal error, and `: ping` comments keep
//     an idle stream open. Every message is replayed first unless
//     after=<message id> names the newest one the reader already holds.
//     An unknown before or after cursor is a JSON 400.
//   - POST /{id}/tokens sizes the session's transcript rows in tokens.
//   - POST /{id}/message sends a ChatMessageRequest to the session: to its live
//     chat run when one is attached in this process, otherwise by resuming the
//     saved session as a new agent-mode chat run. It answers 202 with a
//     ChatMessageResponse.
//   - GET /{id}/chat answers the SessionChat projection of the session's live
//     chat run in this process.
//   - POST /{id}/interrupt pauses the live chat's active turn (a
//     ChatInterruptResponse); POST /{id}/permission-mode switches its posture
//     from a ChatPermissionModeRequest (a ChatPermissionModeResponse). Both
//     answer 202, and 409 when the session has no live chat in this process.
//
// The chat, interrupt and permission-mode routes resolve {id} as a captain or
// provider session id, like the message route, and answer errors as JSON {error}.
func SessionHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{id}", handleSessionRead)
	mux.HandleFunc("POST /{id}/tokens", handleSessionTokens)
	mux.Handle("POST /{id}/message", handleSessionMessage(promptChats))
	mux.Handle("GET /{id}/chat", handleSessionChat(promptChats))
	mux.Handle("POST /{id}/interrupt", handleSessionChatControl(promptChats, interruptChat))
	mux.Handle("POST /{id}/permission-mode", handleSessionChatControl(promptChats, switchChatPermissionMode))
	return mux
}

func handleSessionRead(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if wantsSessionStream(r) {
		streamSession(w, r, id)
		return
	}
	opts, err := sessionWindowFromQuery(id, r)
	if err != nil {
		writeSessionError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	opts.Primary = true
	result, err := RunSessionGet(r.Context(), opts)
	if err != nil {
		writeSessionError(w, sessionErrorStatus(err), err.Error(), nil)
		return
	}
	item := result.Sessions[0]
	if item.Detail == nil {
		writeSessionError(w, http.StatusInternalServerError,
			fmt.Sprintf("session %s has no composed aggregate", item.CaptainID), nil)
		return
	}
	writeChatJSON(w, http.StatusOK, sessionReadResponse{Session: item.Detail, DetailAvailable: item.DetailAvailable, DetailSource: item.DetailSource})
}

func wantsSessionStream(r *http.Request) bool {
	if follow := r.URL.Query().Get("follow"); follow != "" {
		enabled, err := strconv.ParseBool(follow)
		return err == nil && enabled
	}
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

func sessionWindowFromQuery(id string, r *http.Request) (SessionGetOptions, error) {
	opts := SessionGetOptions{ID: id}
	for name, target := range map[string]*int{"tail": &opts.Tail, "offset": &opts.Offset, "limit": &opts.Limit} {
		raw := strings.TrimSpace(r.URL.Query().Get(name))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return SessionGetOptions{}, fmt.Errorf("invalid %s %q: want a non-negative integer", name, raw)
		}
		*target = value
	}
	opts.Before = strings.TrimSpace(r.URL.Query().Get("before"))
	return opts, nil
}

func sessionErrorStatus(err error) int {
	switch {
	case errors.Is(err, database.ErrSessionNotFound):
		return http.StatusNotFound
	case errors.Is(err, database.ErrSessionConflict):
		return http.StatusConflict
	case errors.Is(err, database.ErrInvalidSession):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeSessionError(w http.ResponseWriter, status int, message string, fields map[string]any) {
	body := map[string]any{"error": message}
	for key, value := range fields {
		body[key] = value
	}
	writeChatJSON(w, status, body)
}

// streamSession follows a session over SSE until it ends or the client leaves.
// Errors before the stream opens are JSON responses, except to a client that
// accepts only an event stream: an EventSource never exposes a non-SSE body,
// so it gets the failure as one `error` frame instead of an unexplained
// connection error. Afterwards errors are always `error` frames.
func streamSession(w http.ResponseWriter, r *http.Request, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeSessionError(w, http.StatusInternalServerError, "the response writer cannot stream", nil)
		return
	}
	events, err := followSession(r, id)
	if err != nil && !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		writeSessionError(w, sessionErrorStatus(err), err.Error(), nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	if err != nil {
		if writeErr := writeSessionFrame(w, sessionquery.FollowEvent{Err: err}); writeErr != nil {
			log.Warnf("captain session stream %s: report %v: %v", id, err, writeErr)
		}
		flusher.Flush()
		return
	}
	writeSessionFrames(w, flusher, r, events)
}

func followSession(r *http.Request, id string) (<-chan sessionquery.FollowEvent, error) {
	db, err := captainDB(r.Context())
	if err != nil {
		return nil, err
	}
	return sessionquery.Follow(r.Context(), db, id, sessionquery.FollowOptions{Replay: true, After: r.URL.Query().Get("after")})
}

func writeSessionFrames(w http.ResponseWriter, flusher http.Flusher, r *http.Request, events <-chan sessionquery.FollowEvent) {
	keepAlive := time.NewTicker(sessionStreamKeepAlive)
	defer keepAlive.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			_, err = fmt.Fprint(w, ": ping\n\n")
		case event, open := <-events:
			if !open {
				return
			}
			err = writeSessionFrame(w, event)
		}
		if err != nil {
			log.Warnf("captain session stream %s stopped: %v", r.PathValue("id"), err)
			return
		}
		flusher.Flush()
	}
}

func writeSessionFrame(w http.ResponseWriter, event sessionquery.FollowEvent) error {
	name, payload := "", any(nil)
	switch {
	case event.Err != nil:
		name, payload = "error", map[string]string{"error": event.Err.Error()}
	case event.Message != nil:
		name, payload = "entry", event.Message
	case event.State != nil:
		name, payload = "state", event.State
	default:
		return errors.New("follow produced an empty event")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s frame: %w", name, err)
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	return err
}
