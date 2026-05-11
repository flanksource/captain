// ABOUTME: Runs YAML fixtures by invoking the claude CLI per Run, N times.
// ABOUTME: Aggregates token/cost/duration metrics and compares against a baseline.

package fixture

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/fixture/kubeproxy"
	"github.com/flanksource/captain/pkg/ai/fixture/mcpproxy"

	// Side-effect import: pkg/ai's init populates the pricing registry with the
	// built-in model catalog so resolveCost works offline (no OpenRouter fetch
	// needed for known models).
	_ "github.com/flanksource/captain/pkg/ai"
)

type Row struct {
	Name        string `json:"name" pretty:"label=Run,width=20,table"`
	Model       string `json:"model" pretty:"label=Model,width=28,table"`
	Status      string `json:"status" pretty:"label=Status,table"`
	Repeat      int    `json:"repeat,omitempty" pretty:"label=N,table"`
	DurationMS  string `json:"durationMs" pretty:"label=Duration,table"`
	DurationStd string `json:"durationStddev,omitempty" pretty:"label=±dur,table"`
	CostUSD     string `json:"costUsd" pretty:"label=Cost,table"`
	Input       int    `json:"inputTokens" pretty:"label=Input,table"`
	Output      int    `json:"outputTokens" pretty:"label=Output,table"`
	CacheRead   int    `json:"cacheReadTokens" pretty:"label=Cache Read,table"`
	CacheWrite  int    `json:"cacheWriteTokens" pretty:"label=Cache Write,table"`
	ToolCalls   int    `json:"toolCalls" pretty:"label=Tools,table"`
	MCPCalls    int    `json:"mcpCalls" pretty:"label=MCP,table"`
	BashCalls   int    `json:"bashCalls" pretty:"label=Bash,table"`

	KubectlCalls    int               `json:"kubectlCalls,omitempty" pretty:"-"`
	KubectlAPICalls int               `json:"kubectlApiCalls,omitempty" pretty:"-"`
	KubectlAPILog   []KubectlAPIEntry `json:"kubectlApiLog,omitempty" pretty:"-"`
	MCPAPICalls     int               `json:"mcpApiCalls,omitempty" pretty:"-"`
	MCPAPILog       []MCPAPIEntry     `json:"mcpApiLog,omitempty" pretty:"-"`
	ToolCallLog     []ToolCallEntry   `json:"toolCallLog,omitempty" pretty:"-"`
	KubectlLogPath  string            `json:"kubectlLogPath,omitempty" pretty:"-"`
	MCPLogPath      string            `json:"mcpLogPath,omitempty" pretty:"-"`

	ToolSummary string `json:"toolSummary,omitempty" pretty:"label=Tool Summary,width=32,table"`
	Speedup     string `json:"speedupVsBaseline,omitempty" pretty:"label=Speedup,table"`
	Cheaper     string `json:"cheaperVsBaseline,omitempty" pretty:"label=Cheaper,table"`
	SessionID   string `json:"sessionId,omitempty" pretty:"label=Session"`
	Result      string `json:"result,omitempty" pretty:"-"`
	Error       string `json:"error,omitempty" pretty:"label=Error"`

	DurationMeanMS  float64 `json:"-" pretty:"-"`
	DurationStdevMS float64 `json:"-" pretty:"-"`
	CostMeanUSD     float64 `json:"-" pretty:"-"`
}

type Result struct {
	Name         string         `json:"name,omitempty" pretty:"label=Fixture"`
	Description  string         `json:"description,omitempty" pretty:"label=Description"`
	Baseline     string         `json:"baseline,omitempty" pretty:"label=Baseline"`
	KubectlProxy string         `json:"kubectlProxy,omitempty" pretty:"-"`
	MCPProxies   []MCPProxyInfo `json:"mcpProxies,omitempty" pretty:"-"`
	Rows         []Row          `json:"rows"`

	Fixture *Fixture `json:"-" pretty:"-"`
}

type Options struct {
	ArtifactDir string
	ClaudeBin   string
	Progress    io.Writer
}

type runEnv struct {
	proxy          *kubeproxy.Proxy
	kubeconfig     string
	kubectlLogPath string

	mcpProxies []*mcpproxy.Proxy
	mcpLogPath string
}

func Execute(ctx context.Context, f *Fixture, opts Options) (*Result, error) {
	kubeProxy, kubeconfig, kubeProxyURL, kubeCleanup, err := setupKubernetesProxy(f, opts)
	if err != nil {
		return nil, err
	}
	defer kubeCleanup()

	mcpProxies, mcpProxyInfo, mcpRewrites, mcpCleanup, err := setupMCPProxiesForFixture(f, opts)
	if err != nil {
		return nil, err
	}
	defer mcpCleanup()

	baselineName := f.Baseline
	rows := make([]Row, 0, len(f.Runs))
	rowMetrics := make(map[string]aggregate, len(f.Runs))

	for i, raw := range f.Runs {
		merged := f.Merge(raw)
		if merged.Name == "" {
			merged.Name = fmt.Sprintf("run-%d", i+1)
		}
		if merged.Prompt == "" {
			return nil, fmt.Errorf("run %q is missing prompt", merged.Name)
		}
		if merged.Model == "" {
			return nil, fmt.Errorf("run %q is missing model", merged.Name)
		}
		if baselineName == "" && i == 0 {
			baselineName = merged.Name
		}

		if rewrites, ok := mcpRewrites[merged.Name]; ok && len(rewrites) > 0 {
			merged.MCPConfig = rewrites
		}

		row, agg, err := runOne(ctx, f, merged, opts, runEnv{
			proxy:      kubeProxy,
			kubeconfig: kubeconfig,
			mcpProxies: mcpProxies,
		}, i+1)
		if err != nil {
			return nil, err
		}
		rowMetrics[merged.Name] = agg
		rows = append(rows, row)
	}

	applyBaselineComparisons(rows, baselineName, rowMetrics)

	return &Result{
		Name:         f.Name,
		Description:  f.Description,
		Baseline:     baselineName,
		KubectlProxy: kubeProxyURL,
		MCPProxies:   mcpProxyInfo,
		Rows:         rows,
		Fixture:      f,
	}, nil
}

// setupKubernetesProxy starts the kubectl reverse proxy and writes its
// kubeconfig if f.CaptureKubernetesProxy is set. Returns a no-op cleanup when
// it isn't, so callers can defer unconditionally.
func setupKubernetesProxy(f *Fixture, opts Options) (*kubeproxy.Proxy, string, string, func(), error) {
	if !f.CaptureKubernetesProxy {
		return nil, "", "", func() {}, nil
	}
	p, err := kubeproxy.Start(f.Kubeconfig)
	if err != nil {
		return nil, "", "", func() {}, fmt.Errorf("starting kubernetes proxy: %w", err)
	}
	proxyDir := filepath.Join(opts.ArtifactDir, ".proxy")
	kc, err := p.WriteKubeconfig(proxyDir)
	if err != nil {
		p.Close()
		return nil, "", "", func() {}, fmt.Errorf("writing proxy kubeconfig: %w", err)
	}
	progressf(opts.Progress, "▶ kubernetes proxy: %s  (KUBECONFIG=%s)", p.URL(), kc)
	return p, kc, p.URL(), p.Close, nil
}

// setupMCPProxiesForFixture wraps setupMCPProxies with progress reporting and
// produces a single cleanup closure for all proxies.
func setupMCPProxiesForFixture(f *Fixture, opts Options) ([]*mcpproxy.Proxy, []MCPProxyInfo, map[string][]string, func(), error) {
	if !f.MCPProxy.Capture {
		return nil, nil, map[string][]string{}, func() {}, nil
	}
	proxies, infos, rewrites, err := setupMCPProxies(f, opts.ArtifactDir)
	if err != nil {
		return nil, nil, nil, func() {}, fmt.Errorf("starting MCP proxies: %w", err)
	}
	if len(infos) > 0 {
		progressf(opts.Progress, "▶ MCP proxy active: %d HTTP server(s)", len(infos))
		for _, info := range infos {
			progressf(opts.Progress, "    %s  %s → %s", info.Server, info.Upstream, info.ProxyURL)
		}
	} else {
		progressf(opts.Progress, "▶ MCP proxy enabled but no HTTP MCP servers found in any run's mcpConfig")
	}
	cleanup := func() {
		for _, p := range proxies {
			p.Close()
		}
	}
	return proxies, infos, rewrites, cleanup, nil
}

// runOne executes all repeats of one run, accumulates an aggregate, and
// builds the corresponding Row. runIdx is 1-based, used only for progress
// labels.
func runOne(ctx context.Context, f *Fixture, merged Run, opts Options, env runEnv, runIdx int) (Row, aggregate, error) {
	reps := merged.Repeat
	if reps <= 0 {
		reps = f.Repeat
	}
	if reps <= 0 {
		reps = 1
	}

	agg := aggregate{}
	var runErr error
	var lastResult string
	var kubectlLogPath, mcpLogPath string

	for iter := 0; iter < reps; iter++ {
		progressf(opts.Progress, "[%d/%d] run %q iteration %d/%d (model=%s)…",
			runIdx, len(f.Runs), merged.Name, iter+1, reps, merged.Model)
		start := time.Now()
		iterEnv := env
		if env.proxy != nil && opts.ArtifactDir != "" {
			iterEnv.kubectlLogPath = filepath.Join(opts.ArtifactDir, fmt.Sprintf("%s-%d.kubectl.jsonl", merged.Name, iter+1))
			kubectlLogPath = iterEnv.kubectlLogPath
		}
		if len(env.mcpProxies) > 0 && opts.ArtifactDir != "" {
			iterEnv.mcpLogPath = filepath.Join(opts.ArtifactDir, fmt.Sprintf("%s-%d.mcp.jsonl", merged.Name, iter+1))
			mcpLogPath = iterEnv.mcpLogPath
		}
		summary, err := executeRun(ctx, f.Dir, merged, opts, iter, iterEnv)
		elapsed := time.Since(start).Round(time.Millisecond)
		switch {
		case err != nil:
			progressf(opts.Progress, "    ↳ FAIL in %s: %s", elapsed, truncate(err.Error(), 120))
		case summary.Success:
			progressf(opts.Progress, "    ↳ ok in %s — %d tools, %s",
				elapsed, summary.ToolCalls, formatUSD(summary.CostUSD))
		default:
			progressf(opts.Progress, "    ↳ finished in %s (no success event)", elapsed)
		}
		agg.add(summary)
		if summary.Result != "" {
			lastResult = summary.Result
		}
		if err != nil && runErr == nil {
			runErr = err
		}
		if err != nil {
			break
		}
	}

	agg.finalize()
	costMean, costEstimated := resolveCost(merged.Model, &agg)
	agg.CostMean = costMean

	row := buildRow(merged, &agg, runErr, lastResult, kubectlLogPath, mcpLogPath, costMean, costEstimated)
	return row, agg, nil
}

func buildRow(merged Run, agg *aggregate, runErr error, lastResult, kubectlLogPath, mcpLogPath string, costMean float64, costEstimated bool) Row {
	row := Row{
		Name:            merged.Name,
		Model:           merged.Model,
		Result:          lastResult,
		Status:          statusText(agg.Success, runErr, agg.Error),
		Repeat:          agg.N,
		DurationMS:      formatDurationMS(agg.DurationMean),
		CostUSD:         formatCostWithEstimate(costMean, costEstimated),
		Input:           agg.Input,
		Output:          agg.Output,
		CacheRead:       agg.CacheRead,
		CacheWrite:      agg.CacheWrite,
		ToolCalls:       agg.ToolCalls,
		MCPCalls:        agg.MCPCalls,
		BashCalls:       agg.BashCalls,
		KubectlCalls:    agg.KubectlCalls,
		KubectlAPICalls: agg.KubectlAPICalls,
		KubectlAPILog:   agg.KubectlAPILog,
		MCPAPICalls:     agg.MCPAPICalls,
		MCPAPILog:       agg.MCPAPILog,
		ToolCallLog:     agg.ToolCallLog,
		KubectlLogPath:  kubectlLogPath,
		MCPLogPath:      mcpLogPath,
		ToolSummary:     formatToolCounts(agg.ToolCounts),
		SessionID:       agg.SessionID,
		DurationMeanMS:  agg.DurationMean,
		DurationStdevMS: agg.DurationStdev,
		CostMeanUSD:     agg.CostMean,
	}
	if agg.N > 1 {
		row.DurationStd = formatDurationMS(agg.DurationStdev)
	}
	if runErr != nil {
		row.Error = runErr.Error()
	} else if agg.Error != "" {
		row.Error = agg.Error
	}
	return row
}

func applyBaselineComparisons(rows []Row, baselineName string, metrics map[string]aggregate) {
	base, ok := metrics[baselineName]
	if !ok {
		return
	}
	for i := range rows {
		cur := metrics[rows[i].Name]
		rows[i].Speedup = formatRatio(base.DurationMean, cur.DurationMean)
		rows[i].Cheaper = formatRatio(base.CostMean, cur.CostMean)
	}
}

func executeRun(parent context.Context, fixtureDir string, run Run, opts Options, iter int, env runEnv) (Summary, error) {
	timeout := defaultRunTimeout
	if run.Timeout != "" {
		parsed, err := time.ParseDuration(run.Timeout)
		if err != nil {
			return Summary{}, fmt.Errorf("invalid timeout for %q: %w", run.Name, err)
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	args := buildClaudeArgs(fixtureDir, run)

	bin := opts.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = fixtureDir
	if run.CWD != "" {
		cmd.Dir = resolvePath(fixtureDir, run.CWD)
	}
	cmd.Env = os.Environ()
	if env.kubeconfig != "" {
		cmd.Env = append(cmd.Env, "KUBECONFIG="+env.kubeconfig)
	}
	for k, v := range run.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(run.Prompt)

	kubectlLog, kubectlCleanup, err := attachKubectlLogger(env, run.Name)
	if err != nil {
		return Summary{}, err
	}
	defer kubectlCleanup()

	mcpLog, mcpCleanup, err := attachMCPLogger(env, run.Name)
	if err != nil {
		return Summary{}, err
	}
	defer mcpCleanup()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Summary{}, fmt.Errorf("stdout pipe for %q: %w", run.Name, err)
	}
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return Summary{}, fmt.Errorf("starting claude for %q: %w", run.Name, err)
	}

	artifact, err := openArtifactFile(opts.ArtifactDir, run.Name, iter)
	if err != nil {
		return Summary{}, err
	}
	if artifact != nil {
		defer artifact.Close()
	}

	summary := Summary{ToolCounts: map[string]int{}}
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, streamScannerInitialBuf), streamScannerMaxBuf)

	progressf(opts.Progress, "    · pid=%d, awaiting first stream-json event…", cmd.Process.Pid)
	hb := &heartbeatState{lastEvent: "—"}
	stopHeartbeat := startHeartbeat(opts.Progress, hb)
	defer stopHeartbeat()

	inflight := map[string]*pendingCall{}
	var artifactErr error

	for scanner.Scan() {
		line := scanner.Bytes()
		if artifact != nil && artifactErr == nil {
			if _, err := artifact.Write(line); err != nil {
				artifactErr = fmt.Errorf("writing artifact for %q: %w", run.Name, err)
			} else if _, err := artifact.Write([]byte{'\n'}); err != nil {
				artifactErr = fmt.Errorf("writing artifact for %q: %w", run.Name, err)
			}
		}
		ev, ok := ParseLine(line)
		if !ok {
			hb.update("malformed")
			continue
		}
		hb.update(eventDescription(ev))
		summary.Apply(ev)
		trackToolCalls(ev, inflight, &summary)
		renderEvent(opts.Progress, ev)
	}

	runErr := cmd.Wait()
	flushPendingCalls(inflight, &summary)
	var syncErr error
	if kubectlLog != nil {
		if err := kubectlLog.Sync(); err != nil {
			syncErr = fmt.Errorf("syncing kubectl api log: %w", err)
		}
	}
	if mcpLog != nil {
		if err := mcpLog.Sync(); err != nil && syncErr == nil {
			syncErr = fmt.Errorf("syncing mcp api log: %w", err)
		}
	}
	if runErr != nil {
		return summary, claudeRunError(run.Name, stderrBuf, summary, runErr)
	}
	if artifactErr != nil {
		return summary, artifactErr
	}
	if syncErr != nil {
		return summary, syncErr
	}
	if kubectlLog != nil {
		summary.KubectlAPILog = readKubectlAPILog(env.kubectlLogPath)
		summary.KubectlAPICalls = len(summary.KubectlAPILog)
		correlateKubectlNetworkRequests(summary.ToolCallLog, summary.KubectlAPILog)
	}
	if mcpLog != nil {
		summary.MCPAPILog = readMCPAPILog(env.mcpLogPath)
		summary.MCPAPICalls = len(summary.MCPAPILog)
		correlateMCPNetworkRequests(summary.ToolCallLog, summary.MCPAPILog)
	}
	return summary, nil
}

// buildClaudeArgs assembles the CLI flags from a merged Run.
func buildClaudeArgs(fixtureDir string, run Run) []string {
	args := []string{"-p", "--verbose", "--output-format", "stream-json", "--model", run.Model}
	if run.NoSessionPersistence == nil || *run.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	}
	if run.System != "" {
		args = append(args, "--system-prompt", run.System)
	}
	if run.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", run.AppendSystemPrompt)
	}
	if mode := effectivePermissionMode(run); mode != "" {
		args = append(args, "--permission-mode", mode)
	}
	if run.Settings != "" {
		args = append(args, "--settings", resolvePath(fixtureDir, run.Settings))
	}
	if run.MaxBudgetUSD != "" {
		args = append(args, "--max-budget-usd", run.MaxBudgetUSD)
	}
	if run.PromptCaching != nil && *run.PromptCaching {
		args = append(args, "--exclude-dynamic-system-prompt-sections")
	}
	args = append(args, "--strict-mcp-config")
	if run.Bare != nil && *run.Bare {
		args = append(args, "--bare")
	}
	if len(run.Tools) > 0 {
		args = append(args, "--tools")
		args = append(args, run.Tools...)
	}
	if len(run.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, run.AllowedTools...)
	}
	if len(run.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, run.DisallowedTools...)
	}
	if len(run.Betas) > 0 {
		args = append(args, "--betas")
		args = append(args, run.Betas...)
	}
	if len(run.AddDir) > 0 {
		args = append(args, "--add-dir")
		for _, dir := range run.AddDir {
			args = append(args, resolvePath(fixtureDir, dir))
		}
	}
	// MCP is opt-in: a run gets MCP servers only if mcpConfig is set. With no
	// mcpConfig, an empty inline config is passed so --strict-mcp-config has
	// something to filter to, blocking ambient .mcp.json and user-level servers.
	mcpConfigs := run.MCPConfig
	if len(mcpConfigs) == 0 {
		mcpConfigs = []string{`{"mcpServers":{}}`}
	}
	args = append(args, "--mcp-config")
	for _, cfg := range mcpConfigs {
		args = append(args, resolveMaybeJSONPath(fixtureDir, cfg))
	}
	if len(run.ExtraArgs) > 0 {
		args = append(args, run.ExtraArgs...)
	}
	return args
}

func attachKubectlLogger(env runEnv, runName string) (*os.File, func(), error) {
	if env.proxy == nil || env.kubectlLogPath == "" {
		return nil, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(env.kubectlLogPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating kubectl log dir for %q: %w", runName, err)
	}
	f, err := os.Create(env.kubectlLogPath)
	if err != nil {
		return nil, nil, fmt.Errorf("creating kubectl log for %q: %w", runName, err)
	}
	env.proxy.SetLogger(kubeproxy.NewRequestLogger(f))
	cleanup := func() {
		env.proxy.SetLogger(nil)
		f.Close()
	}
	return f, cleanup, nil
}

func attachMCPLogger(env runEnv, runName string) (*os.File, func(), error) {
	if len(env.mcpProxies) == 0 || env.mcpLogPath == "" {
		return nil, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(env.mcpLogPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating mcp log dir for %q: %w", runName, err)
	}
	f, err := os.Create(env.mcpLogPath)
	if err != nil {
		return nil, nil, fmt.Errorf("creating mcp log for %q: %w", runName, err)
	}
	logger := mcpproxy.NewRequestLogger(f)
	for _, p := range env.mcpProxies {
		p.SetLogger(logger)
	}
	cleanup := func() {
		for _, p := range env.mcpProxies {
			p.SetLogger(nil)
		}
		f.Close()
	}
	return f, cleanup, nil
}

func openArtifactFile(artifactDir, runName string, iter int) (*os.File, error) {
	if artifactDir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating artifact dir for %q: %w", runName, err)
	}
	name := fmt.Sprintf("%s-%d.jsonl", runName, iter+1)
	f, err := os.Create(filepath.Join(artifactDir, name))
	if err != nil {
		return nil, fmt.Errorf("creating artifact for %q: %w", runName, err)
	}
	return f, nil
}

func claudeRunError(runName string, stderrBuf *bytes.Buffer, summary Summary, runErr error) error {
	msg := strings.TrimSpace(stderrBuf.String())
	if msg == "" {
		msg = strings.TrimSpace(summary.Result)
	}
	if msg == "" {
		msg = strings.TrimSpace(summary.Error)
	}
	if msg == "" {
		msg = runErr.Error()
	}
	return fmt.Errorf("claude run %q failed: %s", runName, msg)
}

// startHeartbeat spawns a goroutine that ticks every heartbeatInterval,
// printing how long the run has been going and what kind of stream-json
// event last arrived. The returned function stops the goroutine.
func startHeartbeat(progress io.Writer, hb *heartbeatState) func() {
	heartbeatStart := time.Now()
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ticker.C:
				lines, last := hb.snapshot()
				elapsed := time.Since(heartbeatStart).Round(time.Second)
				progressf(progress, "    · still running (%s elapsed, %d lines, last: %s)", elapsed, lines, last)
			}
		}
	}()
	return func() {
		select {
		case <-heartbeatDone:
		default:
			close(heartbeatDone)
		}
	}
}
