// ABOUTME: A mock Anthropic Messages API — the endpoint Claude Code and the claude-agent SDK talk to.
// ABOUTME: Standalone: Start it, export Env(), and a real `claude` binary runs against scripted replies.

package anthropicmock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/captain/pkg/aimock"
)

// Options configures a mock server.
type Options struct {
	// Scenario supplies the scripted replies. Required.
	Scenario *aimock.Scenario
	// Addr is the listen address; empty picks a free loopback port.
	Addr string
	// JournalPath mirrors every served request to a JSONL file.
	JournalPath string
	// Lenient answers an unmatched request with a bland reply instead of
	// failing. The default (strict) returns aimock.MissStatus with a diagnostic
	// naming the request and every unconsumed rule, so a scenario gap surfaces
	// as a test failure rather than as a passing run against the wrong reply.
	Lenient bool
}

// Server is a mock Anthropic Messages API.
type Server struct {
	aimock.Base
	rules   *aimock.Rules[Respond]
	lenient bool
}

var _ aimock.Server = (*Server)(nil)

// Start binds a mock server and begins serving the scenario's `anthropic`
// section. The caller closes it.
func Start(opts Options) (*Server, error) {
	if opts.Scenario == nil {
		return nil, fmt.Errorf("anthropicmock: Options.Scenario is required")
	}

	var fallback *Respond
	if opts.Lenient {
		fallback = fallbackRespond()
	}
	rules, err := aimock.Section(opts.Scenario, aimock.SectionAnthropic, fallback)
	if err != nil {
		return nil, err
	}

	journal, err := aimock.NewJournal(aimock.SectionAnthropic, opts.JournalPath)
	if err != nil {
		return nil, err
	}

	srv := &Server{rules: rules, lenient: opts.Lenient}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", srv.handleMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", srv.handleCountTokens)
	mux.HandleFunc("GET /v1/models", srv.handleModels)
	mux.HandleFunc("HEAD /{$}", srv.handleHealth)
	mux.HandleFunc("/", srv.handleUnknown)

	if err := srv.Listen(opts.Addr, mux, journal); err != nil {
		journal.Close()
		return nil, err
	}
	return srv, nil
}

// APIURL is the base an Anthropic client should call — what belongs in
// api.Config.APIURL. It carries no /v1 suffix: the SDK and Claude Code both
// append the full versioned path to it.
func (s *Server) APIURL() string { return s.URL() }

// Env points an Anthropic client at the server rooted at rootURL. Beyond the
// base URL and a dummy credential it disables every non-essential call Claude
// Code would otherwise make, so a mocked run touches nothing but the mock. It
// takes a URL rather than a server so a caller can render the environment for a
// known address without binding it.
func Env(rootURL string) []string {
	return []string{
		"ANTHROPIC_BASE_URL=" + rootURL,
		"ANTHROPIC_API_KEY=" + aimock.DummyKey,
		"ANTHROPIC_AUTH_TOKEN=" + aimock.DummyKey,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_NON_ESSENTIAL_MODEL_CALLS=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_BUG_COMMAND=1",
	}
}

// Env points an Anthropic client at this server.
func (s *Server) Env() []string { return Env(s.URL()) }

// Remaining lists scenario rules that never fired — assert it is empty to prove
// a run played the whole scenario.
func (s *Server) Remaining() []string { return s.rules.Remaining() }

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, r, aimock.Request{}, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	wire, norm, err := decodeRequest(r, body)
	if err != nil {
		s.writeError(w, r, norm, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	respond, err := s.rules.Next(norm)
	if err != nil {
		s.writeError(w, r, norm, aimock.MissStatus, "invalid_request_error", err.Error())
		return
	}
	if respond.Error != nil {
		status := respond.Error.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		errType := respond.Error.Type
		if errType == "" {
			errType = "api_error"
		}
		s.writeError(w, r, norm, status, errType, respond.Error.Message)
		return
	}

	model := wire.Model
	if model == "" {
		model = "claude-mock"
	}

	if wire.Stream {
		// The 200 and the first frames are already on the wire by the time a
		// stream can fail, so there is no status left to set — the note goes in
		// the journal, where a test asserting on Requests() will see it.
		note := ""
		cancelled := false
		if err := streamMessage(r.Context(), w, model, respond); err != nil {
			cancelled = aimock.IsClientCancellation(r.Context(), err)
			if !cancelled {
				note = fmt.Sprintf("stream aborted: %v", err)
				logger.Errorf("anthropicmock: %s", note)
			}
		}
		s.recordOutcome(r, norm, http.StatusOK, note, cancelled)
		return
	}

	s.record(r, norm, http.StatusOK, "")
	writeJSON(w, http.StatusOK, messageResponse{
		ID:         messageID(model),
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		Content:    respond.blocks(),
		StopReason: respond.resolvedStopReason(),
		Usage:      respond.Usage.wire(),
	})
}

// handleCountTokens answers the token-estimate probe. The estimate is a crude
// byte count: nothing in captain asserts on it, but the endpoint must exist or
// the client errors before it ever reaches /v1/messages.
func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, r, aimock.Request{}, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	_, norm, err := decodeRequest(r, body)
	if err != nil {
		s.writeError(w, r, norm, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.record(r, norm, http.StatusOK, "")
	writeJSON(w, http.StatusOK, map[string]int{"input_tokens": len(norm.AllText())/4 + 1})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	s.record(r, aimock.Request{}, http.StatusOK, "")
	type model struct {
		Type        string `json:"type"`
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": []model{
			{Type: "model", ID: "claude-sonnet-5", DisplayName: "Claude Sonnet 5 (mock)", CreatedAt: "2025-01-01T00:00:00Z"},
			{Type: "model", ID: "claude-opus-5", DisplayName: "Claude Opus 5 (mock)", CreatedAt: "2025-01-01T00:00:00Z"},
			{Type: "model", ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5 (mock)", CreatedAt: "2025-01-01T00:00:00Z"},
		},
		"has_more": false,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.record(r, aimock.Request{}, http.StatusOK, "")
	w.WriteHeader(http.StatusOK)
}

// handleUnknown fails loudly on an unrouted path rather than 404-ing quietly,
// so a client reaching for an endpoint the mock does not implement shows up as
// a named gap instead of an opaque client-side error.
func (s *Server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	message := fmt.Sprintf("anthropicmock does not implement %s %s", r.Method, r.URL.Path)
	s.record(r, aimock.Request{}, http.StatusNotFound, message)
	writeJSON(w, http.StatusNotFound, newErrorResponse("not_found_error", message))
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, norm aimock.Request, status int, errType, message string) {
	s.record(r, norm, status, message)
	writeJSON(w, status, newErrorResponse(errType, message))
}

func (s *Server) record(r *http.Request, norm aimock.Request, status int, miss string) {
	s.recordOutcome(r, norm, status, miss, false)
}

func (s *Server) recordOutcome(r *http.Request, norm aimock.Request, status int, miss string, cancelled bool) {
	s.Journal().Record(aimock.Recorded{
		Method:    r.Method,
		Path:      r.URL.Path,
		Status:    status,
		Stream:    norm.Stream,
		Model:     norm.Model,
		Request:   norm,
		Miss:      miss,
		Cancelled: cancelled,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
