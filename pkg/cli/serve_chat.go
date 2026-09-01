package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/attachments"
	clickyaichat "github.com/flanksource/clicky/aichat"
	"github.com/flanksource/commons-db/shell"
	"github.com/spf13/cobra"
)

func newCaptainChatService(
	ctx context.Context,
	rootCmd *cobra.Command,
	opts ServeOptions,
	cwd string,
	authority aichat.ExecutionAuthority,
	attachmentStore *attachments.Store,
) (*aichat.Service, *aichat.MCPToolProvider, error) {
	// The ordering is declared here, where captain hands its own commands over:
	// the method, then anything the operation declared about itself, then this
	// app's read-only naming convention. It is handed to the provider so it
	// travels with the command tree, and to the service, which is what resolves it
	// across every tool source at once.
	chatStrategies := []api.PermissionStrategy{
		api.HTTPVerbStrategy{}, api.MCPHintStrategy{}, captainReadOnlyStrategy{},
	}
	chatTools, err := clickyaichat.NewCobraToolProvider(clickyaichat.CobraToolProviderOptions{
		Root: rootCmd, Filter: captainChatToolEnabled, Strategies: chatStrategies,
	})
	if err != nil {
		return nil, nil, err
	}
	mcpTools := aichat.NewMCPToolProvider(aichat.MCPToolProviderOptions{Servers: opts.MCPServers})
	if _, err := mcpTools.ToolSet(ctx); err != nil {
		return nil, nil, err
	}
	chat := aichat.NewService(aichat.ServiceOptions{
		ToolStrategies: chatTools.Strategies(),
		ToolPolicy:     chatTools.ToolPolicy(),
		Profile: aichat.RuntimeProfileProviderFunc(func(context.Context) (aichat.RuntimeProfile, error) {
			resolved, err := api.ResolveSpecLayers(api.SpecLayer{
				Name: "captain serve", Scope: api.SpecLayerGlobal,
				Spec: api.Spec{
					Model: api.Model{Name: "sol", Mode: registry.ModeAgent},
					Setup: &shell.Setup{Cwd: cwd},
				},
			})
			if err != nil {
				return aichat.RuntimeProfile{}, err
			}
			return aichat.RuntimeProfile{
				System: "You are Captain's coding-agent launcher assistant. Use Captain and Clicky tools when useful, " +
					"prefer read-only inspection unless the user explicitly asks for edits, and keep follow-up guidance concise.",
				Resolved: resolved,
			}, nil
		}),
		// Thread reads follow the request's database context; writes never reach
		// a secondary because the context middleware rejects unsafe methods.
		Tools: chatTools, MCP: mcpTools,
		Threads:     aichat.ThreadStoreProviderFunc(contextThreadStore),
		Authority:   authority,
		Attachments: chatAttachmentResolver{store: attachmentStore},
	})
	return chat, mcpTools, nil
}

func handleThreadFromAgent(store aichat.ThreadStore) http.HandlerFunc {
	type request struct {
		Title             string `json:"title"`
		ProviderSessionID string `json:"providerSessionId"`
		Model             string `json:"model"`
	}
	type response struct {
		*aichat.Thread
		LaunchURL string `json:"launchUrl"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
			return
		}
		body.ProviderSessionID = strings.TrimSpace(body.ProviderSessionID)
		if body.ProviderSessionID == "" {
			http.Error(w, "providerSessionId is required", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Title) == "" {
			body.Title = "Captain agent " + body.ProviderSessionID
		}
		thread, err := store.Create(r.Context(), body.Title)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := store.SetProviderSession(r.Context(), thread.ID, body.ProviderSessionID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		thread, err = store.Get(r.Context(), thread.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		launchURL := "/chat/" + url.PathEscape(thread.ID)
		if model := strings.TrimSpace(body.Model); model != "" {
			launchURL += "?model=" + url.QueryEscape(model)
		}
		writeServeJSON(w, http.StatusCreated, response{Thread: thread, LaunchURL: launchURL})
	}
}

func captainChatToolEnabled(tool tools.ToolInfo) bool {
	raw := strings.ToLower(strings.TrimSpace(tool.OperationName()))
	if raw == "" {
		raw = strings.ToLower(strings.TrimSpace(tool.Name))
	}
	raw = strings.ReplaceAll(raw, "/", " ")
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-'
	})
	if len(fields) == 0 {
		return true
	}
	switch fields[0] {
	case "serve", "mcp", "hook", "container", "sandbox", "port", "configure", "ai":
		return false
	default:
		return true
	}
}

// captainReadOnlyStrategy auto-runs the operations captain's own CLI names as
// reads. That CLI predates the entity annotations, so many of its commands carry
// no verb and no safety hint, and a word in the name is the only thing that says
// what they do.
//
// It is a guess, which is why it lives here as this app's own layer rather than
// among the shared strategies: an HTTP method and a declared hint are facts about
// an operation, "a command called list-sessions probably lists" is not. It sits
// last in captain's chain so it can promote a tool the method alone would have
// asked about — a POST named list-sessions still reads.
//
// A strategy rather than a rule list because the match is per word-segment:
// "checkout" must not count as "check", and glob patterns have no way to say so.
type captainReadOnlyStrategy struct{}

func (captainReadOnlyStrategy) Resolve(tool tools.ToolInfo) (api.ToolPolicy, bool) {
	if isReadOnlyCaptainTool(tool.Verb()) ||
		isReadOnlyCaptainTool(tool.Name) ||
		isReadOnlyCaptainTool(tool.OperationName()) ||
		isReadOnlyCaptainTool(tool.Path()) {
		return api.ToolPolicyAllow, true
	}
	return api.ToolPolicyAuto, false
}

func isReadOnlyCaptainTool(value string) bool {
	for _, part := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == '_' || r == '-' || r == '/' || r == '.' || r == ' '
	}) {
		switch part {
		case "list", "get", "read", "show", "lookup", "search", "history", "info", "cost", "changes", "models", "status", "check":
			return true
		}
	}
	return false
}
