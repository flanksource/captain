// ABOUTME: A mock OpenAI API — the endpoint codex and captain's genkit openai/deepseek backends talk to.
// ABOUTME: Standalone: Start it, export Env(), and a real `codex` binary runs against scripted replies.

package openaimock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"

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

// Server is a mock OpenAI API serving both wire APIs from one scenario section.
type Server struct {
	aimock.Base
	rules    *aimock.Rules[Respond]
	sequence atomic.Uint64
}

var _ aimock.Server = (*Server)(nil)

// Start binds a mock server and begins serving the scenario's `openai` section.
// The caller closes it.
func Start(opts Options) (*Server, error) {
	if opts.Scenario == nil {
		return nil, fmt.Errorf("openaimock: Options.Scenario is required")
	}

	var fallback *Respond
	if opts.Lenient {
		fallback = fallbackRespond()
	}
	rules, err := aimock.Section(opts.Scenario, aimock.SectionOpenAI, fallback)
	if err != nil {
		return nil, err
	}

	journal, err := aimock.NewJournal(aimock.SectionOpenAI, opts.JournalPath)
	if err != nil {
		return nil, err
	}

	srv := &Server{rules: rules}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/responses", srv.handleResponses)
	mux.HandleFunc("POST /v1/chat/completions", srv.handleChatCompletions)
	mux.HandleFunc("GET /v1/models", srv.handleModels)
	mux.HandleFunc("/", srv.handleUnknown)

	if err := srv.Listen(opts.Addr, mux, journal); err != nil {
		journal.Close()
		return nil, err
	}
	return srv, nil
}

// APIURL is the versioned base an OpenAI client should call — what belongs in
// api.Config.APIURL.
//
// codex needs this one rather than Env(): with ChatGPT-account auth stored it
// routes to the ChatGPT backend and ignores OPENAI_BASE_URL entirely, so
// captain redirects it through a model_providers override built from APIURL
// (see codexProviderOverride in pkg/ai/provider).
func (s *Server) APIURL() string { return APIURL(s.URL()) }

// APIURL is the versioned base for a server rooted at rootURL. The suffix lives
// here rather than at the call site so callers only ever handle host:port.
func APIURL(rootURL string) string { return rootURL + "/v1" }

// Env points an OpenAI client at the server rooted at rootURL. It covers the
// genkit backends and every SDK client; see APIURL for the codex caveat. It
// takes a URL rather than a server so a caller can render the environment for a
// known address without binding it.
func Env(rootURL string) []string {
	return []string{
		"OPENAI_BASE_URL=" + APIURL(rootURL),
		"OPENAI_API_KEY=" + aimock.DummyKey,
	}
}

// Env points an OpenAI client at this server.
func (s *Server) Env() []string { return Env(s.URL()) }

// Remaining lists scenario rules that never fired — assert it is empty to prove
// a run played the whole scenario.
func (s *Server) Remaining() []string { return s.rules.Remaining() }

func (s *Server) nextWireID(kind, model string) string {
	return fmt.Sprintf("%s_mock_%s_%d", kind, model, s.sequence.Add(1))
}

// resolve picks the scripted reply for a request, writing the miss diagnostic or
// the scripted error itself and reporting false when there is nothing to serve.
func (s *Server) resolve(w http.ResponseWriter, r *http.Request, norm aimock.Request) (Respond, bool) {
	respond, err := s.rules.Next(norm)
	if err != nil {
		s.writeError(w, r, norm, aimock.MissStatus, "invalid_request_error", err.Error())
		return Respond{}, false
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
		s.record(r, norm, status, respond.Error.Message)
		writeJSON(w, status, newErrorResponse(errType, respond.Error.Code, respond.Error.Message))
		return Respond{}, false
	}
	return respond, true
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	s.record(r, aimock.Request{}, http.StatusOK, "")
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int    `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []model{
			{ID: "gpt-5", Object: "model", OwnedBy: "captain-mock"},
			{ID: "gpt-5-codex", Object: "model", OwnedBy: "captain-mock"},
			{ID: "o4-mini", Object: "model", OwnedBy: "captain-mock"},
		},
	})
}

// handleUnknown fails loudly on an unrouted path rather than 404-ing quietly, so
// a client reaching for an endpoint the mock does not implement shows up as a
// named gap instead of an opaque client-side error.
func (s *Server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	message := fmt.Sprintf("openaimock does not implement %s %s", r.Method, r.URL.Path)
	s.record(r, aimock.Request{}, http.StatusNotFound, message)
	writeJSON(w, http.StatusNotFound, newErrorResponse("invalid_request_error", "unknown_route", message))
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, norm aimock.Request, status int, errType, message string) {
	s.record(r, norm, status, message)
	writeJSON(w, status, newErrorResponse(errType, "", message))
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
