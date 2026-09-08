package cli

import (
	"context"
	"io"

	"github.com/flanksource/captain/pkg/agentbrowser"
	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons/logger"
)

const (
	browserStateLive  = agentbrowser.StateLive
	browserStateStale = agentbrowser.StateStale
)

type BrowserListOptions = agentbrowser.BrowserListOptions
type BrowserInstallOptions = agentbrowser.BrowserInstallOptions
type BrowserInstallResult = agentbrowser.BrowserInstallResult
type BrowserLink = agentbrowser.BrowserLink
type BrowserLinkSource = agentbrowser.BrowserLinkSource
type BrowserProcess = agentbrowser.BrowserProcess

const (
	BrowserLinkNone     = agentbrowser.BrowserLinkNone
	BrowserLinkEnv      = agentbrowser.BrowserLinkEnv
	BrowserLinkSidecar  = agentbrowser.BrowserLinkSidecar
	BrowserLinkAncestor = agentbrowser.BrowserLinkAncestor
)

type BrowserSession agentbrowser.BrowserSession

type BrowserListResult struct {
	SocketDir string           `json:"socketDir" pretty:"label=Socket dir"`
	Total     int              `json:"total" pretty:"label=Total"`
	Live      int              `json:"live" pretty:"label=Live"`
	Stale     int              `json:"stale,omitempty" pretty:"label=Stale"`
	CPU       string           `json:"cpu,omitempty" pretty:"label=CPU"`
	Memory    string           `json:"memory,omitempty" pretty:"label=Memory"`
	Sessions  []BrowserSession `json:"sessions"`
}

func RunBrowserList(ctx context.Context, opts BrowserListOptions) (BrowserListResult, error) {
	stopScan := rpchttp.Track(ctx, "scan")
	socketDir, sessions, err := agentbrowser.Discover(ctx)
	stopScan()
	if err != nil {
		return BrowserListResult{}, err
	}
	stopAugment := rpchttp.Track(ctx, "augment")
	augmentBrowserSessionsFromDB(ctx, sessions)
	stopAugment()
	return browserListResult(agentbrowser.BuildList(socketDir, sessions, opts)), nil
}

func RunBrowserInstall(ctx context.Context, opts BrowserInstallOptions) (BrowserInstallResult, error) {
	return agentbrowser.RunBrowserInstall(ctx, opts)
}

func RunBrowserPlugin(stdin io.Reader, stdout, stderr io.Writer, stateDir string) error {
	return agentbrowser.RunBrowserPlugin(stdin, stdout, stderr, stateDir)
}

func BrowserLinkStateDir() (string, error) {
	return agentbrowser.BrowserLinkStateDir()
}

func browserListResult(result agentbrowser.BrowserListResult) BrowserListResult {
	sessions := make([]BrowserSession, len(result.Sessions))
	for i := range result.Sessions {
		sessions[i] = BrowserSession(result.Sessions[i])
	}
	return BrowserListResult{
		SocketDir: result.SocketDir, Total: result.Total, Live: result.Live, Stale: result.Stale,
		CPU: result.CPU, Memory: result.Memory, Sessions: sessions,
	}
}

func augmentBrowserSessionsFromDB(ctx context.Context, sessions []agentbrowser.BrowserSession) {
	db, err := captainDB(ctx)
	if err != nil {
		return
	}
	for i := range sessions {
		sessionID := sessions[i].Link.AgentSessionID
		if sessionID == "" {
			continue
		}
		overview, err := db.GetSessionOverviewByIdentity(ctx, sessionID)
		if err != nil {
			continue
		}
		sessions[i].CaptainSessionID = overview.ID.String()
		sessions[i].Title = stringOr(overview.Title, "")
		sessions[i].Project = stringOr(overview.Project, "")
		if sessions[i].Link.CWD == "" {
			sessions[i].Link.CWD = stringOr(overview.CWD, "")
		}
	}
}

func attachBrowserSessions(ctx context.Context, items []SessionGetItem) {
	if len(items) == 0 {
		return
	}
	_, sessions, err := agentbrowser.Discover(ctx)
	if err != nil {
		logger.Debugf("inspect agent-browser sessions: %v", err)
		return
	}
	for i := range items {
		for _, browser := range sessions {
			if browser.State == agentbrowser.StateLive && browserBelongsToSession(browser, items[i]) {
				items[i].Browsers = append(items[i].Browsers, BrowserSession(browser))
			}
		}
	}
}

func browserBelongsToSession(browser agentbrowser.BrowserSession, item SessionGetItem) bool {
	sessionID := browser.Link.AgentSessionID
	return sessionID != "" && (sessionID == item.ProviderSessionID || sessionID == item.CaptainID)
}
