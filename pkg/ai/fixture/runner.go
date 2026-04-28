// ABOUTME: Runs YAML fixtures by invoking the claude CLI per Run, N times.
// ABOUTME: Aggregates token/cost/duration metrics and compares against a baseline.

package fixture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/fixture/kubeproxy"
	"github.com/flanksource/captain/pkg/ai/pricing"

	// Side-effect import: pkg/ai's init populates pricing registry with the
	// built-in model catalog so resolveCost works offline (no OpenRouter fetch
	// needed for known models).
	_ "github.com/flanksource/captain/pkg/ai"
)

type Row struct {
	Name             string   `json:"name" pretty:"label=Run,width=20,table"`
	Model            string   `json:"model" pretty:"label=Model,width=28,table"`
	Status           string   `json:"status" pretty:"label=Status,table"`
	Repeat           int      `json:"repeat,omitempty" pretty:"label=N,table"`
	DurationMS       string   `json:"durationMs" pretty:"label=Duration,table"`
	DurationStd      string   `json:"durationStddev,omitempty" pretty:"label=±dur,table"`
	CostUSD          string   `json:"costUsd" pretty:"label=Cost,table"`
	Input            int      `json:"inputTokens" pretty:"label=Input,table"`
	Output           int      `json:"outputTokens" pretty:"label=Output,table"`
	CacheRead        int      `json:"cacheReadTokens" pretty:"label=Cache Read,table"`
	CacheWrite       int      `json:"cacheWriteTokens" pretty:"label=Cache Write,table"`
	ToolCalls        int      `json:"toolCalls" pretty:"label=Tools,table"`
	MCPCalls         int      `json:"mcpCalls" pretty:"label=MCP,table"`
	BashCalls        int      `json:"bashCalls" pretty:"label=Bash,table"`
	KubectlCalls      int               `json:"kubectlCalls,omitempty" pretty:"-"`
	KubectlAPICalls   int               `json:"kubectlApiCalls,omitempty" pretty:"-"`
	KubectlAPILog     []KubectlAPIEntry `json:"kubectlApiLog,omitempty" pretty:"-"`
	ToolCallLog       []ToolCallEntry   `json:"toolCallLog,omitempty" pretty:"-"`
	KubectlLogPath    string            `json:"kubectlLogPath,omitempty" pretty:"-"`
	ToolSummary      string   `json:"toolSummary,omitempty" pretty:"label=Tool Summary,width=32,table"`
	Speedup          string   `json:"speedupVsBaseline,omitempty" pretty:"label=Speedup,table"`
	Cheaper          string   `json:"cheaperVsBaseline,omitempty" pretty:"label=Cheaper,table"`
	SessionID        string   `json:"sessionId,omitempty" pretty:"label=Session"`
	Result           string   `json:"result,omitempty" pretty:"-"`
	Error            string   `json:"error,omitempty" pretty:"label=Error"`

	DurationMeanMS  float64 `json:"-" pretty:"-"`
	DurationStdevMS float64 `json:"-" pretty:"-"`
	CostMeanUSD     float64 `json:"-" pretty:"-"`
}

type Result struct {
	Name          string `json:"name,omitempty" pretty:"label=Fixture"`
	Description   string `json:"description,omitempty" pretty:"label=Description"`
	Baseline      string `json:"baseline,omitempty" pretty:"label=Baseline"`
	KubectlProxy  string `json:"kubectlProxy,omitempty" pretty:"-"`
	Rows          []Row  `json:"rows"`

	Fixture *Fixture `json:"-" pretty:"-"`
}

type Options struct {
	ArtifactDir string
	ClaudeBin   string
	Progress    io.Writer
}

func Execute(ctx context.Context, f *Fixture, opts Options) (*Result, error) {
	baselineName := f.Baseline
	rows := make([]Row, 0, len(f.Runs))
	rowMetrics := make(map[string]aggregate, len(f.Runs))

	var proxy *kubeproxy.Proxy
	var proxyKubeconfig string
	var proxyURL string
	if f.CaptureKubernetesProxy {
		p, err := kubeproxy.Start(f.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("starting kubernetes proxy: %w", err)
		}
		defer p.Close()
		proxyDir := filepath.Join(opts.ArtifactDir, ".proxy")
		kc, err := p.WriteKubeconfig(proxyDir)
		if err != nil {
			return nil, fmt.Errorf("writing proxy kubeconfig: %w", err)
		}
		proxy = p
		proxyKubeconfig = kc
		proxyURL = p.URL()
		progressf(opts.Progress, "▶ kubernetes proxy: %s  (KUBECONFIG=%s)", proxyURL, kc)
	}

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
		var kubectlLogPath string
		for iter := 0; iter < reps; iter++ {
			progressf(opts.Progress, "[%d/%d] run %q iteration %d/%d (model=%s)…",
				i+1, len(f.Runs), merged.Name, iter+1, reps, merged.Model)
			start := time.Now()
			runOpts := runEnv{proxy: proxy, kubeconfig: proxyKubeconfig}
			if proxy != nil && opts.ArtifactDir != "" {
				runOpts.kubectlLogPath = filepath.Join(opts.ArtifactDir, fmt.Sprintf("%s-%d.kubectl.jsonl", merged.Name, iter+1))
				kubectlLogPath = runOpts.kubectlLogPath
			}
			summary, err := executeRun(ctx, f.Dir, merged, opts, iter, runOpts)
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
		costMean, costEstimated := resolveCost(merged.Model, agg)
		agg.CostMean = costMean
		rowMetrics[merged.Name] = agg

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
			KubectlCalls:      agg.KubectlCalls,
			KubectlAPICalls:   agg.KubectlAPICalls,
			KubectlAPILog:     agg.KubectlAPILog,
			ToolCallLog:       agg.ToolCallLog,
			KubectlLogPath:    kubectlLogPath,
			ToolSummary:     formatToolCounts(agg.ToolCounts),
			SessionID:       agg.SessionID,
			DurationMeanMS:  agg.DurationMean,
			DurationStdevMS: agg.DurationStdev,
			CostMeanUSD:     agg.CostMean,
		}
		if reps > 1 {
			row.DurationStd = formatDurationMS(agg.DurationStdev)
		}
		if runErr != nil {
			row.Error = runErr.Error()
		} else if agg.Error != "" {
			row.Error = agg.Error
		}
		rows = append(rows, row)
	}

	if base, ok := rowMetrics[baselineName]; ok {
		for i := range rows {
			cur := rowMetrics[rows[i].Name]
			rows[i].Speedup = formatRatio(base.DurationMean, cur.DurationMean)
			rows[i].Cheaper = formatRatio(base.CostMean, cur.CostMean)
		}
	}

	return &Result{
		Name:         f.Name,
		Description:  f.Description,
		Baseline:     baselineName,
		KubectlProxy: proxyURL,
		Rows:         rows,
		Fixture:      f,
	}, nil
}

type runEnv struct {
	proxy          *kubeproxy.Proxy
	kubeconfig     string
	kubectlLogPath string
}

func executeRun(parent context.Context, fixtureDir string, run Run, opts Options, iter int, env runEnv) (Summary, error) {
	timeout := 5 * time.Minute
	if run.Timeout != "" {
		parsed, err := time.ParseDuration(run.Timeout)
		if err != nil {
			return Summary{}, fmt.Errorf("invalid timeout for %q: %w", run.Name, err)
		}
		timeout = parsed
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

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

	var kubectlLog *os.File
	var kubectlLogger *kubeproxy.RequestLogger
	if env.proxy != nil && env.kubectlLogPath != "" {
		if err := os.MkdirAll(filepath.Dir(env.kubectlLogPath), 0o755); err != nil {
			return Summary{}, fmt.Errorf("creating kubectl log dir for %q: %w", run.Name, err)
		}
		f, err := os.Create(env.kubectlLogPath)
		if err != nil {
			return Summary{}, fmt.Errorf("creating kubectl log for %q: %w", run.Name, err)
		}
		kubectlLog = f
		kubectlLogger = kubeproxy.NewRequestLogger(f)
		env.proxy.SetLogger(kubectlLogger)
		defer func() {
			env.proxy.SetLogger(nil)
			f.Close()
		}()
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Summary{}, fmt.Errorf("stdout pipe for %q: %w", run.Name, err)
	}
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return Summary{}, fmt.Errorf("starting claude for %q: %w", run.Name, err)
	}

	var artifact *os.File
	if opts.ArtifactDir != "" {
		if err := os.MkdirAll(opts.ArtifactDir, 0o755); err != nil {
			return Summary{}, fmt.Errorf("creating artifact dir for %q: %w", run.Name, err)
		}
		name := fmt.Sprintf("%s-%d.jsonl", run.Name, iter+1)
		f, err := os.Create(filepath.Join(opts.ArtifactDir, name))
		if err != nil {
			return Summary{}, fmt.Errorf("creating artifact for %q: %w", run.Name, err)
		}
		artifact = f
		defer artifact.Close()
	}

	summary := Summary{ToolCounts: map[string]int{}}
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	progressf(opts.Progress, "    · pid=%d, awaiting first stream-json event…", cmd.Process.Pid)
	heartbeatStart := time.Now()
	heartbeatDone := make(chan struct{})
	hb := &heartbeatState{lastEvent: "—"}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ticker.C:
				lines, last := hb.snapshot()
				elapsed := time.Since(heartbeatStart).Round(time.Second)
				progressf(opts.Progress, "    · still running (%s elapsed, %d lines, last: %s)", elapsed, lines, last)
			}
		}
	}()
	defer close(heartbeatDone)

	inflight := map[string]*pendingCall{}

	for scanner.Scan() {
		line := scanner.Bytes()
		if artifact != nil {
			artifact.Write(line)
			artifact.Write([]byte{'\n'})
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
	if kubectlLog != nil {
		kubectlLog.Sync()
	}
	if runErr != nil {
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
		return summary, fmt.Errorf("claude run %q failed: %s", run.Name, msg)
	}
	if kubectlLog != nil {
		summary.KubectlAPILog = readKubectlAPILog(env.kubectlLogPath)
		summary.KubectlAPICalls = len(summary.KubectlAPILog)
		correlateKubectlNetworkRequests(summary.ToolCallLog, summary.KubectlAPILog)
	}
	return summary, nil
}

// pendingCall is a tool_use awaiting its matching tool_result. We snapshot
// timing, the call label, and the size of the input arguments at issuance
// time; the matching tool_result supplies the output payload size. Tokens are
// rough text-length estimates (~4 chars/token), matching the convention used
// by `captain history`.
type pendingCall struct {
	Time        time.Time
	ToolName    string
	Command     string
	InputTokens int
	IsKubectl   bool
}

// estimateTokens approximates token count from text length using the same
// 4-chars-per-token heuristic as pkg/claude/cost.EstimateTokens.
func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}

// trackToolCalls walks an event's content blocks: assistant tool_use opens an
// inflight call; user tool_result closes one and appends a finalized
// ToolCallEntry to the summary.
func trackToolCalls(ev Event, inflight map[string]*pendingCall, summary *Summary) {
	switch ev.Type {
	case "assistant":
		for _, c := range ev.Content {
			if c.Type != "tool_use" || c.ID == "" {
				continue
			}
			cmd := ""
			isKube := false
			if c.Name == "Bash" {
				cmd = strings.TrimSpace(bashCommandFrom(c.Input))
				isKube = isKubectlCommand(cmd)
			}
			inflight[c.ID] = &pendingCall{
				Time:        time.Now().UTC(),
				ToolName:    c.Name,
				Command:     cmd,
				InputTokens: estimateTokens(string(c.Input)),
				IsKubectl:   isKube,
			}
		}
	case "user":
		for _, c := range ev.Content {
			if c.Type != "tool_result" || c.ToolUseID == "" {
				continue
			}
			pc, ok := inflight[c.ToolUseID]
			if !ok {
				continue
			}
			delete(inflight, c.ToolUseID)
			end := time.Now().UTC()
			dur := end.Sub(pc.Time)
			output := ToolResultText(c.Content)
			summary.ToolCallLog = append(summary.ToolCallLog, ToolCallEntry{
				Time:         pc.Time,
				EndTime:      end,
				ToolName:     pc.ToolName,
				Command:      pc.Command,
				Duration:     dur.Round(time.Millisecond).String(),
				DurationMS:   float64(dur) / float64(time.Millisecond),
				InputTokens:  pc.InputTokens,
				OutputTokens: estimateTokens(output),
				Output:       output,
				IsKubectl:    pc.IsKubectl,
			})
		}
	}
}

// flushPendingCalls finalizes any tool_uses that never got a matching
// tool_result (claude exited mid-call, malformed stream, etc.) so we don't
// silently drop them from the per-run log. Duration and output are unknown.
func flushPendingCalls(inflight map[string]*pendingCall, summary *Summary) {
	for _, pc := range inflight {
		summary.ToolCallLog = append(summary.ToolCallLog, ToolCallEntry{
			Time:        pc.Time,
			EndTime:     pc.Time,
			ToolName:    pc.ToolName,
			Command:     pc.Command,
			Duration:    "-",
			InputTokens: pc.InputTokens,
			IsKubectl:   pc.IsKubectl,
		})
	}
	for k := range inflight {
		delete(inflight, k)
	}
}

// correlateKubectlNetworkRequests counts proxy API requests whose timestamps
// fall inside each kubectl Bash call's tool_use→tool_result window.
func correlateKubectlNetworkRequests(calls []ToolCallEntry, api []KubectlAPIEntry) {
	for i := range calls {
		c := &calls[i]
		if !c.IsKubectl {
			continue
		}
		for _, req := range api {
			if !req.Time.Before(c.Time) && !req.Time.After(c.EndTime) {
				c.NetworkRequests++
			}
		}
	}
}

type heartbeatState struct {
	mu        sync.Mutex
	lines     int
	lastEvent string
}

func (h *heartbeatState) update(desc string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines++
	h.lastEvent = desc
}

func (h *heartbeatState) snapshot() (int, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lines, h.lastEvent
}

// eventDescription summarizes a stream-json event for the heartbeat line so
// users can see what claude is doing between renders (esp. tool_results which
// renderEvent suppresses to keep output clean).
func eventDescription(ev Event) string {
	switch ev.Type {
	case "system":
		if ev.Subtype != "" {
			return "system " + ev.Subtype
		}
		return "system"
	case "assistant":
		for _, c := range ev.Content {
			switch c.Type {
			case "tool_use":
				return "tool_use " + c.Name
			case "text":
				return "assistant text"
			}
		}
		return "assistant"
	case "user":
		return "tool_result"
	case "result":
		if ev.Subtype != "" {
			return "result " + ev.Subtype
		}
		return "result"
	}
	if ev.Type != "" {
		return ev.Type
	}
	return "—"
}

// ToolCallEntry is one tool_use/tool_result pair captured from the stream:
// when the model issued the call, what it issued, how long it took, what came
// back, and how many tokens the parent assistant turn cost. NetworkRequests is
// populated only for kubectl Bash invocations once the run completes and the
// proxy log has been correlated by timestamp window.
type ToolCallEntry struct {
	Time            time.Time `json:"time"`
	EndTime         time.Time `json:"endTime"`
	ToolName        string    `json:"toolName"`
	Command         string    `json:"command,omitempty"`
	Duration        string    `json:"duration"`
	DurationMS      float64   `json:"durationMs"`
	InputTokens     int       `json:"inputTokens"`
	OutputTokens    int       `json:"outputTokens"`
	Output          string    `json:"output,omitempty"`
	NetworkRequests int       `json:"networkRequests,omitempty"`
	IsKubectl       bool      `json:"isKubectl,omitempty"`
}

// Label returns what to show in the unified tool-call table's command column:
// the literal bash command for Bash, the tool name for everything else.
func (e ToolCallEntry) Label() string {
	if e.ToolName == "Bash" {
		return e.Command
	}
	return e.ToolName
}

// KubectlAPIEntry is one HTTP request observed flowing through the proxy.
type KubectlAPIEntry struct {
	Time     time.Time `json:"time"`
	Method   string    `json:"method"`
	URL      string    `json:"url"`
	Status   int       `json:"status"`
	Duration string    `json:"duration"`
	Bytes    int64     `json:"bytes"`
}

func (e KubectlAPIEntry) Format() string {
	return fmt.Sprintf("%s  %s %s  %d  %s  %s",
		e.Time.Local().Format("15:04:05.000"), e.Method, e.URL, e.Status, e.Duration, humanBytes(e.Bytes))
}

// readKubectlAPILog parses a kubectl.jsonl file into chronological API entries.
// Tool calls themselves are captured live from the stream-json output.
func readKubectlAPILog(path string) []KubectlAPIEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	var api []KubectlAPIEntry
	for scanner.Scan() {
		var ev struct {
			Type     string    `json:"type"`
			Time     time.Time `json:"time"`
			Method   string    `json:"method"`
			Path     string    `json:"path"`
			Query    string    `json:"query"`
			Status   int       `json:"status"`
			Duration string    `json:"duration"`
			Bytes    int64     `json:"bytes"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type != "request" {
			continue
		}
		url := ev.Path
		if ev.Query != "" {
			url += "?" + ev.Query
		}
		api = append(api, KubectlAPIEntry{
			Time: ev.Time, Method: ev.Method, URL: url,
			Status: ev.Status, Duration: ev.Duration, Bytes: ev.Bytes,
		})
	}
	return api
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffix := "KMGTPE"[exp]
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), suffix)
}

// effectivePermissionMode demotes bypassPermissions to default whenever the
// run specifies an allowedTools whitelist: Claude CLI's --allowedTools is an
// auto-approve list, not a restriction, so under bypassPermissions the model
// can still reach for any tool. Demoting to default turns --allowedTools into
// an actual allowlist in non-interactive -p mode (anything else gets denied).
func effectivePermissionMode(run Run) string {
	if len(run.AllowedTools) > 0 && (run.PermissionMode == "" || run.PermissionMode == "bypassPermissions") {
		return "default"
	}
	return run.PermissionMode
}

func renderEvent(w io.Writer, ev Event) {
	if w == nil {
		return
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" && ev.SessionID != "" {
			fmt.Fprintf(w, "      · session %s\n", ev.SessionID)
		}
	case "assistant":
		for _, c := range ev.Content {
			switch c.Type {
			case "text":
				text := strings.TrimSpace(c.Text)
				if text == "" {
					continue
				}
				fmt.Fprintf(w, "      💭 %s\n", truncate(text, 140))
			case "tool_use":
				fmt.Fprintf(w, "      → %s %s\n", c.Name, summarizeToolInput(c.Name, c.Input))
			}
		}
	case "user":
		// tool_result events — rendered by the tool_use arrow already, skip to avoid noise.
	}
}

func summarizeToolInput(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal(input, &decoded); err != nil {
		return ""
	}
	// Prefer the field most characteristic of each tool.
	keyPriority := map[string][]string{
		"Bash":  {"command"},
		"Read":  {"file_path", "path"},
		"Write": {"file_path", "path"},
		"Edit":  {"file_path", "path"},
		"Glob":  {"pattern", "path"},
		"Grep":  {"pattern", "path"},
	}
	keys := keyPriority[name]
	if strings.HasPrefix(name, "mcp__") {
		keys = []string{"query", "name", "id", "namespace"}
	}
	for _, k := range keys {
		if v, ok := decoded[k]; ok {
			return truncate(fmt.Sprint(v), 120)
		}
	}
	// Fallback: first key alphabetically for stability.
	keysSorted := make([]string, 0, len(decoded))
	for k := range decoded {
		keysSorted = append(keysSorted, k)
	}
	sort.Strings(keysSorted)
	if len(keysSorted) > 0 {
		return truncate(fmt.Sprintf("%s=%v", keysSorted[0], decoded[keysSorted[0]]), 120)
	}
	return ""
}

type aggregate struct {
	N               int
	Success         bool
	Error           string
	SessionID       string
	DurationMean    float64
	DurationStdev   float64
	CostMean        float64
	Input           int
	Output          int
	CacheRead       int
	CacheWrite      int
	ToolCalls       int
	MCPCalls        int
	BashCalls       int
	KubectlCalls      int
	KubectlAPICalls   int
	KubectlAPILog     []KubectlAPIEntry
	ToolCallLog       []ToolCallEntry
	ToolCounts        map[string]int

	durations []float64
	costs     []float64
}

func (a *aggregate) add(s Summary) {
	a.N++
	a.durations = append(a.durations, s.DurationMS)
	a.costs = append(a.costs, s.CostUSD)
	if s.SessionID != "" {
		a.SessionID = s.SessionID
	}
	if s.Error != "" && a.Error == "" {
		a.Error = s.Error
	}
	if s.Success {
		a.Success = true
	}
	// Accumulate token/tool counts (sums across repeats — avg derived by dividing in formatter if needed).
	a.Input += s.Input
	a.Output += s.Output
	a.CacheRead += s.CacheRead
	a.CacheWrite += s.CacheWrite
	a.ToolCalls += s.ToolCalls
	a.MCPCalls += s.MCPCalls
	a.BashCalls += s.BashCalls
	a.KubectlCalls += s.KubectlCalls
	a.KubectlAPICalls += s.KubectlAPICalls
	a.KubectlAPILog = append(a.KubectlAPILog, s.KubectlAPILog...)
	a.ToolCallLog = append(a.ToolCallLog, s.ToolCallLog...)
	if a.ToolCounts == nil {
		a.ToolCounts = map[string]int{}
	}
	for k, v := range s.ToolCounts {
		a.ToolCounts[k] += v
	}
}

func (a *aggregate) finalize() {
	if a.N == 0 {
		return
	}
	a.DurationMean = mean(a.durations)
	a.DurationStdev = stdev(a.durations, a.DurationMean)
	a.CostMean = mean(a.costs)
	// Per-iteration averages for token/tool aggregate counts.
	a.Input /= a.N
	a.Output /= a.N
	a.CacheRead /= a.N
	a.CacheWrite /= a.N
	a.ToolCalls /= a.N
	a.MCPCalls /= a.N
	a.BashCalls /= a.N
	a.KubectlCalls /= a.N
	a.KubectlAPICalls /= a.N
	for k, v := range a.ToolCounts {
		a.ToolCounts[k] = v / a.N
	}
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func stdev(xs []float64, m float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var s float64
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

func resolvePath(baseDir, value string) string {
	if value == "" {
		return value
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(baseDir, value)
}

func resolveMaybeJSONPath(baseDir, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return value
	}
	return resolvePath(baseDir, value)
}

func statusText(success bool, err error, resultErr string) string {
	if err != nil || resultErr != "" || !success {
		return "FAIL"
	}
	return "OK"
}

func formatDurationMS(ms float64) string {
	if ms <= 0 {
		return "-"
	}
	return time.Duration(ms * float64(time.Millisecond)).Round(time.Millisecond).String()
}

func formatUSD(v float64) string {
	if v == 0 {
		return "$0"
	}
	return fmt.Sprintf("$%.4f", v)
}

// resolveCost returns the per-iteration cost for a row. If the underlying
// claude CLI didn't report a cost (CostMean == 0) we fall back to the
// OpenRouter-backed pricing registry to estimate from the token counts.
// Returns (cost, estimated) — estimated is true when the value is from the
// fallback path.
func resolveCost(model string, a aggregate) (float64, bool) {
	if a.CostMean > 0 {
		return a.CostMean, false
	}
	if a.Input == 0 && a.Output == 0 && a.CacheRead == 0 && a.CacheWrite == 0 {
		return 0, false
	}
	res, err := pricing.CalculateCost(model, a.Input, a.Output, 0, a.CacheRead, a.CacheWrite)
	if err != nil {
		return 0, false
	}
	return res.TotalCost, true
}

func formatCostWithEstimate(v float64, estimated bool) string {
	s := formatUSD(v)
	if estimated && v > 0 {
		return s + " (est)"
	}
	return s
}

func formatToolCounts(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s:%d", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func progressf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, format+"\n", args...)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func formatRatio(base, current float64) string {
	if base <= 0 || current <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2fx", base/current)
}
