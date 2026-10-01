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
	sessionquery "github.com/flanksource/captain/pkg/session/query"
)

// sessionStreamKeepAlive is how often an idle follow stream sends an SSE
// comment so proxies and browsers keep the connection open.
var sessionStreamKeepAlive = 15 * time.Second

// SessionHandler serves one Captain session by identity, with paths relative to
// wherever a host mounts it (captain serve and gavel mount it at
// /api/captain/sessions via http.StripPrefix):
//
//   - GET /{id} returns the unified session.Session that `captain sessions get`
//     composes, with the whole transcript unless tail, offset or limit narrow it.
//   - GET /{id}?follow=1, or with Accept: text/event-stream, follows the session
//     over SSE: `entry` frames carry a session.Message upsert, `state` frames a
//     FollowState, `error` frames a terminal error, and `: ping` comments keep
//     an idle stream open.
func SessionHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{id}", handleSessionRead)
	return mux
}

// registerSessionRoutes mounts SessionHandler beside the session message route.
// The method-qualified message pattern is more specific than the mount's
// prefix, so both coexist on one mux.
func registerSessionRoutes(mux *http.ServeMux, chats *chatBroker) {
	mux.Handle("POST /api/captain/sessions/{id}/message", handleSessionMessage(chats))
	mux.Handle("/api/captain/sessions/", http.StripPrefix("/api/captain/sessions", SessionHandler()))
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
	result, err := RunSessionGet(r.Context(), opts)
	if err != nil {
		writeSessionError(w, sessionErrorStatus(err), err.Error(), nil)
		return
	}
	item, status, err := primarySessionItem(id, result)
	if err != nil {
		writeSessionError(w, status, err.Error(), nil)
		return
	}
	if item.Detail == nil {
		writeSessionError(w, http.StatusNotFound,
			fmt.Sprintf("session %s has no transcript, prompt or stored messages to show", item.CaptainID),
			map[string]any{"detailSource": item.DetailSource})
		return
	}
	writeChatJSON(w, http.StatusOK, item.Detail)
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
	return opts, nil
}

// primarySessionItem is the item the identity names: the only one, or the
// root of the thread a root id expanded to.
func primarySessionItem(id string, result SessionGetResult) (SessionGetItem, int, error) {
	switch {
	case result.Total == 0:
		return SessionGetItem{}, http.StatusNotFound, fmt.Errorf("session %s not found", id)
	case len(result.Sessions) == 1:
		return result.Sessions[0], 0, nil
	}
	for _, item := range result.Sessions {
		if result.RootSessionID != "" && item.CaptainID == result.RootSessionID {
			return item, 0, nil
		}
	}
	return SessionGetItem{}, http.StatusConflict,
		fmt.Errorf("session id %s is ambiguous: it names %d unrelated sessions", id, len(result.Sessions))
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
// Errors before the stream opens are JSON responses; afterwards they are
// `error` frames.
func streamSession(w http.ResponseWriter, r *http.Request, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeSessionError(w, http.StatusInternalServerError, "the response writer cannot stream", nil)
		return
	}
	db, err := captainDB(r.Context())
	if err != nil {
		writeSessionError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	events, err := sessionquery.Follow(r.Context(), db, id, sessionquery.FollowOptions{Replay: true})
	if err != nil {
		writeSessionError(w, sessionErrorStatus(err), err.Error(), nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	writeSessionFrames(w, flusher, r, events)
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
