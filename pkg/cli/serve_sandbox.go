// HTTP surface for sandbox configuration and the git-agent roster.
//
// These are bespoke handlers rather than the auto-generated /api/v1 executor
// routes, for reasons the executor cannot satisfy: `add` mints a single-use
// join token and `revoke` rewrites ~/.captain.yaml, so both must sit behind
// validateLocalConfigurationRequest; and the webapp needs the raw result
// structs, not clicky's rendered command output. The git-agent command group is
// marked local-only (cmd/captain/main.go) precisely so this is the only surface.

package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/database"
)

const defaultSandboxBackend = "git-agent"

func registerSandboxHandlers(mux *http.ServeMux) {
	mux.Handle("GET /api/captain/sandboxes", handleSandboxCatalog())
	mux.Handle("GET /api/captain/sandbox/git-agent/agents", handleGitAgentList())
	mux.Handle("POST /api/captain/sandbox/git-agent/agents", handleGitAgentAdd())
	mux.Handle("POST /api/captain/sandbox/git-agent/agents/{name}/whoami", handleGitAgentWhoami())
	mux.Handle("DELETE /api/captain/sandbox/git-agent/agents/{name}", handleGitAgentRevoke())
	mux.Handle("GET /api/captain/sandbox/git-agent/tasks", handleGitAgentTaskList())
	mux.Handle("GET /api/captain/sandbox/git-agent/tasks/{taskId}", handleGitAgentTaskGet())
	registerSandboxDeployHandlers(mux)
	registerSandboxCredentialHandlers(mux)
}

// handleGitAgentTaskList serves remote-run history from the database, which the
// ingest watcher fills from the supervisor's mailbox tree.
func handleGitAgentTaskList() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		db, err := captainDB(r.Context())
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusServiceUnavailable))
			return
		}
		query := r.URL.Query()
		limit := 0
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			limit, _ = strconv.Atoi(raw)
		}
		tasks, err := db.ListGitAgentTasks(r.Context(), database.ListGitAgentTasksFilter{
			Backend: strings.TrimSpace(query.Get("backend")),
			Agent:   strings.TrimSpace(query.Get("agent")),
			Status:  database.GitAgentTaskStatus(strings.TrimSpace(query.Get("status"))),
			Limit:   limit,
		})
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, tasks)
	})
}

// handleGitAgentTaskGet serves one task with its per-attempt verdicts. A task id
// is unique only within its mailbox, so ?mailbox= disambiguates when one id
// exists in more than one.
func handleGitAgentTaskGet() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		taskID := strings.TrimSpace(r.PathValue("taskId"))
		if taskID == "" {
			http.Error(w, "task id is required", http.StatusBadRequest)
			return
		}
		db, err := captainDB(r.Context())
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusServiceUnavailable))
			return
		}
		detail, ok, err := db.GetGitAgentTask(r.Context(),
			strings.TrimSpace(r.URL.Query().Get("mailbox")), taskID)
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		if !ok {
			http.Error(w, fmt.Sprintf("task %q not found", taskID), http.StatusNotFound)
			return
		}
		writeServeJSON(w, http.StatusOK, detail)
	})
}

// sandboxBackendParam resolves the ?backend= selector every git-agent route
// accepts, defaulting to the same backend name the CLI flags default to.
func sandboxBackendParam(r *http.Request) string {
	if backend := strings.TrimSpace(r.URL.Query().Get("backend")); backend != "" {
		return backend
	}
	return defaultSandboxBackend
}

// handleSandboxCatalog serves the same projection the prompt schema embeds, from
// the same builder, so /api/captain/sandboxes and promptSchema.sandboxes cannot
// drift apart.
func handleSandboxCatalog() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeServeJSON(w, http.StatusOK, buildSandboxCatalog(loadSavedConfig().Sandbox))
	})
}

func handleGitAgentList() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := RunGitAgentList(GitAgentListOptions{Backend: sandboxBackendParam(r)})
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	})
}

// gitAgentAddRequest is the enrollment body. Endpoint is optional: empty falls
// back to the backend's configured url, matching the CLI flag.
type gitAgentAddRequest struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint,omitempty"`
	DryRun   bool   `json:"dryRun,omitempty"`
}

func handleGitAgentAdd() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateLocalConfigurationRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var request gitAgentAddRequest
		if err := decodeServeJSONBody(w, r, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(request.Name) == "" {
			http.Error(w, "agent name is required", http.StatusBadRequest)
			return
		}
		result, err := RunGitAgentAdd(r.Context(), GitAgentAddOptions{
			Name:     request.Name,
			Backend:  sandboxBackendParam(r),
			Endpoint: request.Endpoint,
			DryRun:   request.DryRun,
		})
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	})
}

func handleGitAgentRevoke() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateLocalConfigurationRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		name := strings.TrimSpace(r.PathValue("name"))
		if name == "" {
			http.Error(w, "agent name is required", http.StatusBadRequest)
			return
		}
		backend := sandboxBackendParam(r)
		result, err := RunGitAgentRevoke(GitAgentRevokeOptions{
			Name:    name,
			Backend: backend,
			DryRun:  strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("dryRun")), "true"),
		})
		if err != nil {
			// "not enrolled" / "no enrolled agents" is a missing resource, not a
			// malformed request; the UI distinguishes them to decide whether to
			// refresh the roster or show a validation error.
			http.Error(w, err.Error(), serveRunStatus(err, gitAgentRevokeStatus(err)))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	})
}

func gitAgentRevokeStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	message := err.Error()
	if strings.Contains(message, "is not enrolled") || strings.Contains(message, "has no enrolled agents") {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// sandboxBodyLimit matches providerTokenBodyLimit: these bodies are a handful
// of short strings, so anything larger is a mistake or an attack.
const sandboxBodyLimit = 8 << 10

// decodeServeJSONBody reads exactly one strict JSON object, mirroring
// decodeProviderTokenRequest: an unknown key is an error rather than a silently
// ignored field, so a typo'd "endpoint" cannot enroll an agent against the
// wrong address.
func decodeServeJSONBody(w http.ResponseWriter, r *http.Request, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, sandboxBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain one JSON object")
	}
	return nil
}
