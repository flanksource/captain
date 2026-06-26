package claude

import (
	"os"
	"path/filepath"
	"testing"
)

const agentTestUUID = "11111111-1111-1111-1111-111111111111"

// writeAgentFixture builds a fake ~/.claude/projects tree for currentDir holding
// one main session (an Agent tool call) plus one nested sub-agent transcript
// (an Edit) with its meta sidecar. Returns the projects dir.
func writeAgentFixture(t *testing.T, currentDir, editPath string) string {
	t.Helper()
	projectsDir := t.TempDir()
	slug := NormalizePath(currentDir)
	sessionDir := filepath.Join(projectsDir, slug)
	subDir := filepath.Join(sessionDir, agentTestUUID, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	main := `{"sessionId":"` + agentTestUUID + `","uuid":"m1","timestamp":"2024-01-15T10:00:00Z","isSidechain":false,` +
		`"message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Agent",` +
		`"input":{"subagent_type":"general-purpose","description":"do thing"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, agentTestUUID+".jsonl"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}

	agent := `{"sessionId":"` + agentTestUUID + `","uuid":"a1","timestamp":"2024-01-15T10:01:00Z","isSidechain":true,"agentId":"abc",` +
		`"message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Edit",` +
		`"input":{"file_path":"` + editPath + `","old_string":"x","new_string":"y"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(subDir, "agent-abc.jsonl"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{"agentType":"general-purpose","description":"do thing","toolUseId":"t1","spawnDepth":1}`
	if err := os.WriteFile(filepath.Join(subDir, "agent-abc.meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectsDir
}

func TestFindAgentTranscripts(t *testing.T) {
	currentDir := "/work/myproj"
	projectsDir := writeAgentFixture(t, currentDir, "/work/myproj/foo.go")

	main, err := FindSessionFiles(projectsDir, currentDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(main) != 1 {
		t.Fatalf("FindSessionFiles: want 1 main session file, got %d (%v)", len(main), main)
	}
	if filepath.Base(main[0]) != agentTestUUID+".jsonl" {
		t.Errorf("FindSessionFiles returned a sub-agent file: %s", main[0])
	}

	agents, err := FindAgentTranscripts(projectsDir, currentDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("findAgentTranscripts: want 1 agent transcript, got %d (%v)", len(agents), agents)
	}
	if filepath.Base(agents[0]) != "agent-abc.jsonl" {
		t.Errorf("findAgentTranscripts returned wrong file: %s", agents[0])
	}
}

func TestParseHistoryIncludeAgents(t *testing.T) {
	currentDir := "/work/myproj"
	editPath := "/work/myproj/foo.go"
	projectsDir := writeAgentFixture(t, currentDir, editPath)

	// ParseHistory resolves projectsDir via GetProjectsDir() ($HOME-derived);
	// rather than fight the HOME layout, assert via the lower-level read path
	// that mirrors ParseHistory's discover→read→stamp pipeline exactly.
	editFound := func(includeAgents bool) (found, sidechain bool, agentType, agentDesc string) {
		files, err := FindSessionFiles(projectsDir, currentDir, false)
		if err != nil {
			t.Fatal(err)
		}
		if includeAgents {
			a, err := FindAgentTranscripts(projectsDir, currentDir, false)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, a...)
		}
		for _, f := range files {
			entries, err := ReadHistoryFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, tu := range stampToolUses(ExtractToolUses(entries), projectsDir, f) {
				if tu.Tool == "Edit" && tu.Input["file_path"] == editPath {
					return true, tu.IsSidechain, tu.AgentType, tu.AgentDesc
				}
			}
		}
		return false, false, "", ""
	}

	if found, _, _, _ := editFound(false); found {
		t.Error("without IncludeAgents the sub-agent Edit must not appear")
	}
	found, sidechain, agentType, agentDesc := editFound(true)
	if !found {
		t.Fatal("with IncludeAgents the sub-agent Edit must appear")
	}
	if !sidechain {
		t.Error("sub-agent Edit should be marked IsSidechain")
	}
	if agentType != "general-purpose" {
		t.Errorf("agentType: want general-purpose, got %q", agentType)
	}
	if agentDesc != "do thing" {
		t.Errorf("agentDesc: want 'do thing', got %q", agentDesc)
	}
}

func TestAgentIDFromPath(t *testing.T) {
	got := agentIDFromPath("/p/sess/subagents/agent-deadbeef.jsonl")
	if got != "deadbeef" {
		t.Errorf("agentIDFromPath: want deadbeef, got %q", got)
	}
}
