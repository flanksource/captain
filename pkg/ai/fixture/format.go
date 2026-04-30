// ABOUTME: Stream-event formatting, progress rendering, heartbeat, and small string/path helpers.
// ABOUTME: All buffer sizes, timeouts, and truncation widths used by the runner are constants here.

package fixture

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tunables shared across the runner. Pulled together so they're easy to find
// and tune without scanning the whole package.
const (
	defaultRunTimeout = 5 * time.Minute

	streamScannerInitialBuf = 1024 * 1024      // 1 MiB
	streamScannerMaxBuf     = 10 * 1024 * 1024 // 10 MiB
	jsonlScannerInitialBuf  = 1024 * 1024
	jsonlScannerMaxBuf      = 10 * 1024 * 1024

	heartbeatInterval = 10 * time.Second

	truncateAssistantText = 140
	truncateToolInput     = 120
)

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
				fmt.Fprintf(w, "      💭 %s\n", truncate(text, truncateAssistantText))
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
			return truncate(fmt.Sprint(v), truncateToolInput)
		}
	}
	keysSorted := make([]string, 0, len(decoded))
	for k := range decoded {
		keysSorted = append(keysSorted, k)
	}
	sort.Strings(keysSorted)
	if len(keysSorted) > 0 {
		return truncate(fmt.Sprintf("%s=%v", keysSorted[0], decoded[keysSorted[0]]), truncateToolInput)
	}
	return ""
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
	if success && err == nil && resultErr == "" {
		return "OK"
	}
	return "FAIL"
}

func formatDurationMS(ms float64) string {
	if ms <= 0 {
		return "-"
	}
	return time.Duration(ms * float64(time.Millisecond)).Round(time.Millisecond).String()
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

func formatRatio(base, current float64) string {
	if base <= 0 || current <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2fx", base/current)
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
