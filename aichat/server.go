package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/spf13/cobra"
)

// Options configures a chat Server.
type Options struct {
	// RootCmd is the Cobra command tree whose operations become AI tools
	// (executed in-process via clicky's RPC executor). Optional.
	RootCmd *cobra.Command
	// MCPServers are external MCP servers consumed as additional tools. Optional.
	MCPServers []MCPServer
	// System is the agent's system prompt. A default is used when empty.
	System string
}

const defaultSystem = "You are an operator assistant for this application. " +
	"Use the available tools to answer questions and perform actions on the user's behalf. " +
	"Prefer calling a tool over guessing. Summarize tool results clearly."

// Server is an AI-SDK-compatible chat backend. It serves POST /api/chat as the
// v6 UI Message Stream protocol, backed by Genkit + clicky operations + MCP.
type Server struct {
	g       *genkit.Genkit
	clicky  *ClickyToolset
	tools   []ai.ToolRef
	system  string
	initErr error
	once    sync.Once
	opts    Options
}

// NewServer builds a chat server. Genkit init and tool discovery happen lazily
// on the first request so construction never fails on missing API keys.
func NewServer(opts Options) *Server {
	system := opts.System
	if system == "" {
		system = defaultSystem
	}
	return &Server{system: system, opts: opts}
}

// Handler returns the http.Handler for POST /api/chat.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/chat", s.handleChat)
	return mux
}

func (s *Server) ensureInit(ctx context.Context) error {
	s.once.Do(func() {
		g, err := initGenkit(ctx)
		if err != nil {
			s.initErr = err
			return
		}
		s.g = g
		if s.opts.RootCmd != nil {
			ts, err := NewClickyToolset(s.opts.RootCmd)
			if err != nil {
				s.initErr = err
				return
			}
			s.clicky = ts
			s.tools = append(s.tools, ts.DefineTools(g)...)
		}
		mcpTools, err := MCPTools(ctx, g, s.opts.MCPServers)
		if err != nil {
			s.initErr = err
			return
		}
		s.tools = append(s.tools, mcpTools...)
	})
	return s.initErr
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := s.ensureInit(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return
	}

	model, err := LookupModel(req.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := ValidateEffort(req.ReasoningEffort); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	msgs, err := toGenkitMessages(req.Messages)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sse, err := newSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.stream(ctx, sse, model, req.ReasoningEffort, msgs); err != nil {
		// Headers are already sent; surface the error as an SSE error part.
		_ = sse.errorPart(err.Error())
	}
	_ = sse.done()
}

// stream runs one chat turn, translating Genkit stream chunks into v6 SSE parts.
// Genkit auto-executes the tool loop; the callback observes text, tool requests
// and tool responses as they flow across steps.
func (s *Server) stream(ctx context.Context, sse *sseWriter, model Model, effort Effort, msgs []*ai.Message) error {
	if err := sse.start(); err != nil {
		return err
	}
	if err := sse.startStep(); err != nil {
		return err
	}

	em := &streamEmitter{sse: sse}
	cb := func(_ context.Context, chunk *ai.ModelResponseChunk) error {
		return em.onChunk(chunk)
	}

	opts := generateOptions(model, effort, s.system, msgs, s.tools, cb)
	if _, err := genkit.Generate(ctx, s.g, opts...); err != nil {
		return err
	}

	if err := em.closeText(); err != nil {
		return err
	}
	if err := sse.finishStep(); err != nil {
		return err
	}
	return sse.finish()
}

// streamEmitter tracks per-turn streaming state: the open text block id and
// seen tool calls, so it emits correctly ordered text-start/-delta/-end and
// tool-input/-output parts.
type streamEmitter struct {
	sse       *sseWriter
	textID    string
	textOpen  bool
	textBlock int
}

func (e *streamEmitter) onChunk(chunk *ai.ModelResponseChunk) error {
	for _, p := range chunk.Content {
		switch {
		case p.IsText() && p.Text != "":
			if err := e.openText(); err != nil {
				return err
			}
			if err := e.sse.textDelta(e.textID, p.Text); err != nil {
				return err
			}
		case p.IsToolRequest() && p.ToolRequest != nil && !p.ToolRequest.Partial:
			if err := e.closeText(); err != nil {
				return err
			}
			tr := p.ToolRequest
			if err := e.sse.toolInputAvailable(tr.Ref, tr.Name, tr.Input); err != nil {
				return err
			}
		case p.IsToolResponse() && p.ToolResponse != nil:
			resp := p.ToolResponse
			if err := e.sse.toolOutputAvailable(resp.Ref, resp.Output); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *streamEmitter) openText() error {
	if e.textOpen {
		return nil
	}
	e.textID = fmt.Sprintf("text-%d", e.textBlock)
	e.textBlock++
	e.textOpen = true
	return e.sse.textStart(e.textID)
}

func (e *streamEmitter) closeText() error {
	if !e.textOpen {
		return nil
	}
	e.textOpen = false
	return e.sse.textEnd(e.textID)
}
