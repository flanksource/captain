package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/gitagent"
)

func TestAgentTaskLogMonitorStreamsOnlyNewTaskActivity(t *testing.T) {
	repo := t.TempDir()
	oldTask := "t-old"
	if err := gitagent.SaveTaskState(repo, &gitagent.TaskState{Task: oldTask}); err != nil {
		t.Fatal(err)
	}
	writeAgentLog(t, repo, oldTask, "agent.stderr.log", "old output\n")

	var stdout, stderr bytes.Buffer
	var notices []string
	monitor := newAgentTaskLogMonitor(repo, &stdout, &stderr, func(format string, args ...any) {
		notices = append(notices, formatMessage(format, args...))
	})
	if err := monitor.prime(); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 || len(notices) != 0 {
		t.Fatalf("prime replayed old activity: stdout=%q stderr=%q notices=%v", stdout.String(), stderr.String(), notices)
	}

	task := "t-new"
	if err := gitagent.SaveTaskState(repo, &gitagent.TaskState{Task: task}); err != nil {
		t.Fatal(err)
	}
	writeAgentLog(t, repo, task, "agent.stdout.log", "model output\n")
	writeAgentLog(t, repo, task, "agent.stderr.log", "normal captain log\n")
	if err := monitor.scan(true); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "model output\n" || stderr.String() != "normal captain log\n" {
		t.Fatalf("new task output was not streamed: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	writeAgentLog(t, repo, task, "agent.stderr.log", "next line\n")
	if _, err := gitagent.UpdateTaskState(repo, task, func(state *gitagent.TaskState) (bool, error) {
		state.Attempts = 1
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := gitagent.SaveVerdict(repo, gitagent.TierVerdict{
		V: 1, Task: task, Attempt: 1, Tier: "sidecar", Status: gitagent.StatusAccepted,
	}); err != nil {
		t.Fatal(err)
	}
	if err := monitor.scan(true); err != nil {
		t.Fatal(err)
	}
	if err := monitor.scan(true); err != nil {
		t.Fatal(err)
	}

	if stderr.String() != "normal captain log\nnext line\n" {
		t.Fatalf("tail replayed or lost bytes: %q", stderr.String())
	}
	joined := strings.Join(notices, "\n")
	for _, want := range []string{
		"git-agent task t-new received",
		"git-agent task t-new submit attempt 1",
		"git-agent task t-new attempt 1 accepted at sidecar",
	} {
		if strings.Count(joined, want) != 1 {
			t.Fatalf("notices %q contain %q %d times, want once", joined, want, strings.Count(joined, want))
		}
	}
}

func writeAgentLog(t *testing.T, repo, task, name, text string) {
	t.Helper()
	dir := filepath.Join(repo, "captain", "tasks", task)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func formatMessage(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
