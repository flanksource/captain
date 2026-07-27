package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
	clickyrpc "github.com/flanksource/clicky/rpc"
	"github.com/google/uuid"
)

// EnvironmentSessionInfo identifies the agent session exported to this process
// and the Captain database sessions bound to that identity.
type EnvironmentSessionInfo struct {
	Source          string          `json:"source"`
	SessionID       string          `json:"sessionId,omitempty"`
	Marker          string          `json:"marker"`
	CaptainSessions []SessionRecord `json:"captainSessions"`
}

type infoSessionStore interface {
	sessionOverviewStore
	ListSessionOverviewsByProviderSessionID(context.Context, string) ([]database.SessionOverview, error)
}

type infoRuntime struct {
	getenv func(string) string
	getwd  func() (string, error)
	openDB func(context.Context) (infoSessionStore, error)
}

// RunInfo reports the active environment session before falling back to
// project-scoped Claude and Codex transcript discovery.
func RunInfo(ctx context.Context, opts InfoOptions) (InfoResult, error) {
	return runInfo(ctx, opts, infoRuntime{
		getenv: os.Getenv,
		getwd:  os.Getwd,
		openDB: func(ctx context.Context) (infoSessionStore, error) { return captainDB(ctx) },
	})
}

func runInfo(ctx context.Context, opts InfoOptions, runtime infoRuntime) (InfoResult, error) {
	if _, ok := clickyrpc.RequestFromContext(ctx); ok && opts.Path != "" {
		workspace, err := runtime.getwd()
		if err != nil {
			return InfoResult{}, fmt.Errorf("resolve info workspace: %w", err)
		}
		opts.Path, err = resolveCatalogDir(workspace, opts.Path)
		if err != nil {
			return InfoResult{}, fmt.Errorf("resolve info path: %w", err)
		}
	}
	if !infoUsesEnvironment(opts) {
		return runInfoDiscovery(opts)
	}
	current := detectEnvironmentSession(runtime.getenv)
	if current == nil {
		return runInfoDiscovery(opts)
	}
	cwd, err := runtime.getwd()
	if err != nil {
		return InfoResult{}, err
	}
	current.CaptainSessions = []SessionRecord{}
	if current.SessionID == "" {
		return InfoResult{CWD: cwd, CurrentSession: current}, nil
	}
	store, err := runtime.openDB(ctx)
	if err != nil {
		return InfoResult{}, fmt.Errorf("resolve %s session %q: %w", current.Source, current.SessionID, err)
	}
	resolved, err := resolveEnvironmentSession(ctx, store, *current)
	if err != nil {
		return InfoResult{}, err
	}
	return InfoResult{CWD: cwd, CurrentSession: &resolved}, nil
}

func infoUsesEnvironment(opts InfoOptions) bool {
	return opts.Path == "" && !opts.All && !opts.Claude && !opts.Codex
}

// CurrentEnvironmentSession returns the active agent session identified by
// Captain's provider environment markers.
func CurrentEnvironmentSession() *EnvironmentSessionInfo {
	return detectEnvironmentSession(os.Getenv)
}

func detectEnvironmentSession(getenv func(string) string) *EnvironmentSessionInfo {
	type marker struct {
		source string
		name   string
		id     bool
		value  string
	}
	markers := []marker{
		{source: "codex", name: "CODEX_THREAD_ID", id: true},
		{source: "codex", name: "CODEX_SESSION_ID", id: true},
		{source: "codex", name: "CODEX_SANDBOX"},
		{source: "claude", name: "CLAUDE_CODE_SESSION_ID", id: true},
		{source: "claude", name: "CLAUDE_SESSION_ID", id: true},
		{source: "claude", name: "CLAUDECODE", value: "1"},
		{source: "gemini", name: "GEMINI_SESSION_ID", id: true},
		{source: "gemini", name: "GEMINI_CLI", value: "1"},
		{source: "captain", name: "CAPTAIN_SESSION_ID", id: true},
	}
	for _, candidate := range markers {
		value := strings.TrimSpace(getenv(candidate.name))
		if value == "" || candidate.value != "" && value != candidate.value {
			continue
		}
		result := &EnvironmentSessionInfo{Source: candidate.source, Marker: candidate.name}
		if candidate.id {
			result.SessionID = value
		}
		return result
	}
	return nil
}

func resolveEnvironmentSession(ctx context.Context, store infoSessionStore, current EnvironmentSessionInfo) (EnvironmentSessionInfo, error) {
	var (
		overviews []database.SessionOverview
		err       error
	)
	if current.Source == "captain" {
		if _, parseErr := uuid.Parse(current.SessionID); parseErr != nil {
			return EnvironmentSessionInfo{}, fmt.Errorf("%s must contain a valid Captain UUID: %w", current.Marker, parseErr)
		}
		overviews, err = resolveOverviewsByIdentity(ctx, store, current.SessionID)
		if errors.Is(err, database.ErrSessionNotFound) {
			overviews, err = nil, nil
		}
	} else {
		overviews, err = store.ListSessionOverviewsByProviderSessionID(ctx, current.SessionID)
	}
	if err != nil {
		return EnvironmentSessionInfo{}, fmt.Errorf("resolve %s session from %s: %w", current.Source, current.Marker, err)
	}
	current.CaptainSessions = make([]SessionRecord, len(overviews))
	for i := range overviews {
		current.CaptainSessions[i] = recordFromOverview(overviews[i])
	}
	return current, nil
}

func (s EnvironmentSessionInfo) Pretty(cwd string) api.Text {
	name := s.Source
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	t := api.Text{}.
		Add(icons.AI).Space().
		Append(name+" session", "font-bold text-blue-600").
		NewLine().Add(infoRow("CWD", cwd)).
		NewLine().Add(infoRow("Detected by", s.Marker))
	if s.SessionID != "" {
		t = t.NewLine().Add(infoRow("Session", s.SessionID))
	}
	t = t.NewLine().NewLine().Append("Captain sessions", "font-bold text-blue-600")
	if len(s.CaptainSessions) == 0 {
		return t.NewLine().Append("  (no matching sessions found)", "text-gray-500 italic")
	}
	rows := make([]sessionLiveRow, len(s.CaptainSessions))
	for i := range s.CaptainSessions {
		rows[i] = sessionLiveRow{SessionRecord: s.CaptainSessions[i]}
	}
	return t.NewLine().Add(api.NewTableFrom(rows))
}
