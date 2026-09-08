package agentbrowser

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	clickyprocess "github.com/flanksource/clicky/process"
)

// Browser session lifecycle states. A session is live while its daemon is
// running; the sidecars of a dead daemon survive until `agent-browser doctor`
// clears them, and captain reports them rather than deleting another tool's
// files.
const (
	StateLive  = "live"
	StateStale = "stale"
)

type BrowserListOptions struct {
	All   bool   `flag:"all" help:"Include sessions whose daemon is no longer running" short:"a"`
	Query string `flag:"q" help:"Filter by session name, namespace, pid, or linked agent session"`
}

type BrowserListResult struct {
	SocketDir string           `json:"socketDir" pretty:"label=Socket dir"`
	Total     int              `json:"total" pretty:"label=Total"`
	Live      int              `json:"live" pretty:"label=Live"`
	Stale     int              `json:"stale,omitempty" pretty:"label=Stale"`
	CPU       string           `json:"cpu,omitempty" pretty:"label=CPU"`
	Memory    string           `json:"memory,omitempty" pretty:"label=Memory"`
	Sessions  []BrowserSession `json:"sessions"`
}

// BrowserSession is one agent-browser session: its daemon, the cost of the
// browser process tree underneath it, and the agent session that launched it.
type BrowserSession struct {
	Session       string           `json:"session"`
	Namespace     string           `json:"namespace,omitempty"`
	State         string           `json:"state"`
	DaemonPID     int              `json:"daemonPid,omitempty"`
	StartedAt     *time.Time       `json:"startedAt,omitempty"`
	CPUPercent    float64          `json:"cpuPercent,omitempty"`
	MemoryPercent float64          `json:"memoryPercent,omitempty"`
	MemoryRSSKB   int64            `json:"memoryRssKb,omitempty"`
	Processes     []BrowserProcess `json:"processes,omitempty"`
	Command       string           `json:"command,omitempty"`
	Engine        string           `json:"engine,omitempty"`
	Version       string           `json:"version,omitempty"`
	Provider      string           `json:"provider,omitempty"`
	SocketPath    string           `json:"socketPath,omitempty"`
	StreamPort    int              `json:"streamPort,omitempty"`
	Link          BrowserLink      `json:"link,omitempty"`
	// CaptainSessionID, Title and Project come from joining Link.AgentSessionID
	// to captain's own session row; they stay empty for an agent session captain
	// has never ingested.
	CaptainSessionID string `json:"captainSessionId,omitempty"`
	Title            string `json:"title,omitempty"`
	Project          string `json:"project,omitempty"`
}

type BrowserProcess struct {
	PID         int     `json:"pid"`
	PPID        int     `json:"ppid,omitempty"`
	Status      string  `json:"status,omitempty"`
	Active      bool    `json:"active"`
	CPUPercent  float64 `json:"cpuPercent,omitempty"`
	MemoryRSSKB int64   `json:"memoryRssKb,omitempty"`
	Command     string  `json:"command,omitempty"`
}

// List reports every agent-browser session on this host.
//
// agent-browser records only a session name and its daemon's pid — no metrics,
// no owner, and no metadata field that could hold one. Everything else here is
// derived: the cost from the daemon's process subtree (the browser is an
// unrecorded child of the daemon), and the owning agent from the daemon's
// inherited environment, captain's launch-plugin record, or the process
// ancestry.
func List(ctx context.Context, opts BrowserListOptions) (BrowserListResult, error) {
	socketDir, sessions, err := Discover(ctx)
	if err != nil {
		return BrowserListResult{}, err
	}
	return BuildList(socketDir, sessions, opts), nil
}

func Discover(ctx context.Context) (string, []BrowserSession, error) {
	socketDir, err := agentBrowserSocketDir()
	if err != nil {
		return "", nil, err
	}
	sidecars, err := scanBrowserSidecars(socketDir)
	if err != nil {
		return "", nil, err
	}

	sessions, err := browserSessionsFromSidecars(ctx, sidecars)
	if err != nil {
		return "", nil, err
	}
	return socketDir, sessions, nil
}

func BuildList(socketDir string, sessions []BrowserSession, opts BrowserListOptions) BrowserListResult {
	sessions = filterBrowserSessions(sessions, opts)
	sortBrowserSessions(sessions)
	return browserListResult(socketDir, sessions)
}

// browserSessionsFromSidecars costs and attributes every discovered session. One
// ps snapshot serves every session; the link inputs are read per daemon.
func browserSessionsFromSidecars(ctx context.Context, sidecars []browserSidecar) ([]BrowserSession, error) {
	if len(sidecars) == 0 {
		return nil, nil
	}
	tree, err := clickyprocess.Discover(ctx, clickyprocess.SnapshotOptions{
		Environment: &clickyprocess.EnvironmentOptions{
			Keys:     []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_PID", "CODEX_THREAD_ID"},
			Prefixes: []string{"CMUX_", "AGENT_BROWSER_"},
		},
	})
	if err != nil {
		return nil, err
	}
	stateDir, err := BrowserLinkStateDir()
	if err != nil {
		return nil, err
	}
	sessions := make([]BrowserSession, 0, len(sidecars))
	for _, sidecar := range sidecars {
		session, err := browserSessionFromSidecar(sidecar, tree, stateDir)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func browserSessionFromSidecar(sidecar browserSidecar, tree *clickyprocess.Snapshot, stateDir string) (BrowserSession, error) {
	record, err := readBrowserLinkRecord(browserLinkPath(stateDir, sidecar.Namespace, sidecar.Session))
	if err != nil {
		return BrowserSession{}, err
	}
	daemon, running := tree.Get(sidecar.DaemonPID)
	session := BrowserSession{
		Session:    sidecar.Session,
		Namespace:  sidecar.Namespace,
		State:      StateStale,
		DaemonPID:  sidecar.DaemonPID,
		Engine:     sidecar.Engine,
		Version:    sidecar.Version,
		Provider:   sidecar.Provider,
		SocketPath: sidecar.SocketPath,
		StreamPort: sidecar.StreamPort,
	}
	if !running {
		session.Link = resolveBrowserLink(sidecar, BrowserLinkInputs{Record: record})
		return session, nil
	}
	usage := tree.AggregateSubtree(sidecar.DaemonPID)
	session.State = StateLive
	session.StartedAt = daemon.StartedAt
	session.Command = daemon.Command
	session.CPUPercent = usage.CPUPercent
	session.MemoryPercent = usage.MemoryPercent
	session.MemoryRSSKB = int64(usage.RSSBytes / 1024)
	for _, process := range usage.Processes {
		session.Processes = append(session.Processes, BrowserProcess{
			PID: process.PID, PPID: process.PPID, Status: process.Status, Active: process.Active,
			CPUPercent: process.CPUPercent, MemoryRSSKB: int64(process.RSSBytes / 1024), Command: process.Command,
		})
	}
	session.Link = resolveBrowserLink(sidecar, BrowserLinkInputs{Env: daemon.Environment, Record: record, Tree: tree})
	return session, nil
}

func filterBrowserSessions(sessions []BrowserSession, opts BrowserListOptions) []BrowserSession {
	filtered := make([]BrowserSession, 0, len(sessions))
	for _, session := range sessions {
		if !opts.All && session.State != StateLive {
			continue
		}
		if !browserSessionMatchesQuery(session, opts.Query) {
			continue
		}
		filtered = append(filtered, session)
	}
	return filtered
}

func browserSessionMatchesQuery(session BrowserSession, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	haystack := []string{
		session.Session, session.Namespace, session.Title, session.Project,
		session.Link.AgentSessionID, session.Link.AgentSource, session.CaptainSessionID,
		strconv.Itoa(session.DaemonPID),
	}
	for _, value := range haystack {
		if value != "" && strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

// sortBrowserSessions puts live sessions first, newest daemon first, so the
// browsers still costing something lead the table.
func sortBrowserSessions(sessions []BrowserSession) {
	sort.SliceStable(sessions, func(i, j int) bool {
		if (sessions[i].State == StateLive) != (sessions[j].State == StateLive) {
			return sessions[i].State == StateLive
		}
		return browserStartedAt(sessions[i]).After(browserStartedAt(sessions[j]))
	})
}

func browserStartedAt(session BrowserSession) time.Time {
	if session.StartedAt == nil {
		return time.Time{}
	}
	return *session.StartedAt
}

func browserListResult(socketDir string, sessions []BrowserSession) BrowserListResult {
	result := BrowserListResult{SocketDir: socketDir, Total: len(sessions), Sessions: sessions}
	var cpu float64
	var rss int64
	for _, session := range sessions {
		if session.State == StateLive {
			result.Live++
		} else {
			result.Stale++
		}
		cpu += session.CPUPercent
		rss += session.MemoryRSSKB
	}
	if cpu > 0 {
		result.CPU = fmt.Sprintf("%.1f%%", cpu)
	}
	if rss > 0 {
		result.Memory = FormatRSS(rss)
	}
	return result
}

// FormatRSS renders a resident-set size given in kilobytes, which is the unit
// ps(1) reports.
func FormatRSS(kb int64) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1fGB", float64(kb)/(1024*1024))
	case kb >= 1024:
		return fmt.Sprintf("%dMB", kb/1024)
	default:
		return fmt.Sprintf("%dKB", kb)
	}
}
