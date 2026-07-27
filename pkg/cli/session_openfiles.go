package cli

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
)

// codexRolloutIDPattern matches the trailing UUID in a codex rollout filename
// (rollout-<timestamp>-<uuid>.jsonl). The timestamp segment never forms a
// UUID group, so the first match is always the session id.
var codexRolloutIDPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// processOpenSessionFiles maps each pid to the session transcript files it holds
// open, via a single `lsof` pass. Enrichment is best-effort: when some pids have
// already exited lsof exits non-zero but still streams the rest, which is parsed.
func processOpenSessionFiles(pids []int) map[int][]string {
	if runtime.GOOS == "windows" {
		return map[int][]string{}
	}
	var list []string
	for _, pid := range pids {
		if pid > 0 {
			list = append(list, strconv.Itoa(pid))
		}
	}
	if len(list) == 0 {
		return map[int][]string{}
	}
	out, _ := exec.Command("lsof", "-n", "-P", "-w", "-F", "pn", "-p", strings.Join(list, ",")).Output()
	return parseLsofOpenFiles(out)
}

// parseLsofOpenFiles parses `lsof -F pn` output into pid → open session-file
// paths, keeping only claude/codex transcript files.
func parseLsofOpenFiles(out []byte) map[int][]string {
	files := make(map[int][]string)
	current := 0
	for _, raw := range bytes.Split(out, []byte{'\n'}) {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			if pid, err := strconv.Atoi(line[1:]); err == nil {
				current = pid
			}
		case 'n':
			if current <= 0 {
				continue
			}
			path := line[1:]
			if source, _, _ := classifyOpenSessionFile(path); source != "" {
				files[current] = append(files[current], path)
			}
		}
	}
	return files
}

// classifyOpenSessionFile identifies a session transcript by path, returning its
// source ("claude"|"codex"), session/agent id, and kind ("root"|"subagent"|
// "codex"). A non-transcript path yields empty strings.
func classifyOpenSessionFile(path string) (source, id, kind string) {
	if !strings.HasSuffix(path, ".jsonl") {
		return "", "", ""
	}
	base := filepath.Base(path)
	if history.IsCodexSession(path) {
		return "codex", codexRolloutID(base), "codex"
	}
	if strings.Contains(path, "/.claude/projects/") {
		if strings.HasPrefix(base, "agent-") || strings.Contains(path, "/subagents/") {
			return "claude", claudeAgentID(base), "subagent"
		}
		return "claude", sessionIDFromFile(path), "root"
	}
	return "", "", ""
}

// codexRolloutID extracts the session UUID from a codex rollout filename.
func codexRolloutID(base string) string {
	if m := codexRolloutIDPattern.FindString(base); m != "" {
		return m
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// claudeAgentID extracts the agent id from a sub-agent transcript filename
// ("agent-<id>.jsonl" → "<id>").
func claudeAgentID(base string) string {
	return strings.TrimPrefix(strings.TrimSuffix(base, ".jsonl"), "agent-")
}
