package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/gitagent"
)

const agentTaskLogPollInterval = 200 * time.Millisecond

// agentTaskLogMonitor bridges detached task output back to the long-running
// sidecar terminal. It tails files rather than inheriting receive-pack's file
// descriptors, which would keep the dispatch push open for the agent's life.
type agentTaskLogMonitor struct {
	repo     string
	stdout   io.Writer
	stderr   io.Writer
	notify   func(string, ...any)
	interval time.Duration
	tasks    map[string]*observedAgentTask
}

type observedAgentTask struct {
	offsets  map[string]int64
	attempts int
	verdicts map[int]bool
}

func newAgentTaskLogMonitor(repo string, stdout, stderr io.Writer, notify func(string, ...any)) *agentTaskLogMonitor {
	return &agentTaskLogMonitor{
		repo: repo, stdout: stdout, stderr: stderr, notify: notify,
		interval: agentTaskLogPollInterval,
		tasks:    map[string]*observedAgentTask{},
	}
}

func (m *agentTaskLogMonitor) prime() error {
	return m.scan(false)
}

func (m *agentTaskLogMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.scan(true); err != nil {
				log.Warnf("git-agent task log monitor: %v", err)
			}
		}
	}
}

func (m *agentTaskLogMonitor) scan(reportNew bool) error {
	root := filepath.Join(m.repo, "captain", "tasks")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := m.scanTask(entry.Name(), reportNew); err != nil {
			return fmt.Errorf("task %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (m *agentTaskLogMonitor) scanTask(task string, reportNew bool) error {
	observed, exists := m.tasks[task]
	if !exists {
		observed = &observedAgentTask{offsets: map[string]int64{}, verdicts: map[int]bool{}}
		m.tasks[task] = observed
		if !reportNew {
			return m.seedTask(task, observed)
		}
		m.notify("git-agent task %s received; workspace=%s", task, filepath.Join(m.repo, "captain", "tasks", task, "worktree"))
	}
	dir := filepath.Join(m.repo, "captain", "tasks", task)
	for _, stream := range []struct {
		name   string
		writer io.Writer
	}{{"agent.stdout.log", m.stdout}, {"agent.stderr.log", m.stderr}} {
		offset, err := copyAppended(filepath.Join(dir, stream.name), observed.offsets[stream.name], stream.writer)
		if err != nil {
			return err
		}
		observed.offsets[stream.name] = offset
	}
	state, found, err := gitagent.LoadTaskState(m.repo, task)
	if err != nil {
		return err
	}
	if found && state.Attempts > observed.attempts {
		for attempt := observed.attempts + 1; attempt <= state.Attempts; attempt++ {
			m.notify("git-agent task %s submit attempt %d", task, attempt)
		}
		observed.attempts = state.Attempts
	}
	return m.scanVerdicts(task, observed)
}

func (m *agentTaskLogMonitor) seedTask(task string, observed *observedAgentTask) error {
	dir := filepath.Join(m.repo, "captain", "tasks", task)
	for _, name := range []string{"agent.stdout.log", "agent.stderr.log"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
			observed.offsets[name] = info.Size()
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if state, found, err := gitagent.LoadTaskState(m.repo, task); err != nil {
		return err
	} else if found {
		observed.attempts = state.Attempts
	}
	entries, err := os.ReadDir(filepath.Join(dir, "verdicts"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if attempt, ok := verdictAttempt(entry.Name()); ok {
			observed.verdicts[attempt] = true
		}
	}
	return nil
}

func (m *agentTaskLogMonitor) scanVerdicts(task string, observed *observedAgentTask) error {
	dir := filepath.Join(m.repo, "captain", "tasks", task, "verdicts")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		attempt, ok := verdictAttempt(entry.Name())
		if !ok || observed.verdicts[attempt] {
			continue
		}
		verdict, found, err := gitagent.LoadVerdict(m.repo, task, attempt)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		message := ""
		if len(verdict.Findings) > 0 && verdict.Findings[0].Message != "" {
			message = ": " + verdict.Findings[0].Message
		}
		m.notify("git-agent task %s attempt %d %s at %s%s", task, attempt, verdict.Status, verdict.Tier, message)
		observed.verdicts[attempt] = true
	}
	return nil
}

func verdictAttempt(name string) (int, bool) {
	if filepath.Ext(name) != ".json" {
		return 0, false
	}
	attempt, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
	return attempt, err == nil && attempt > 0
}

func copyAppended(path string, offset int64, dst io.Writer) (int64, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return offset, nil
	}
	if err != nil {
		return offset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return offset, err
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	written, err := io.Copy(dst, file)
	return offset + written, err
}
