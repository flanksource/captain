package process

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	clickyprocess "github.com/flanksource/clicky/process"
)

// InspectOptions selects the processes Inspect reports on.
type InspectOptions struct {
	PIDs []int
}

// Inspect reports one Agent per requested PID, whatever that process turns out
// to be. Unlike Discover it never filters on Source: a named PID may be an
// agent-browser daemon, an agent that was never ingested, or a plain shell, and
// the caller asked for that specific process rather than for "the agents".
//
// A PID with no live process is an error — naming a process that does not exist
// is a mistake worth reporting, not an empty result.
func Inspect(ctx context.Context, opts InspectOptions) ([]Agent, error) {
	if len(opts.PIDs) == 0 {
		return nil, nil
	}
	// Empty selectors capture every variable, so a session id carried in an
	// environment variable this package does not know by name still surfaces.
	snapshot, err := clickyprocess.Discover(ctx, clickyprocess.SnapshotOptions{
		Environment: &clickyprocess.EnvironmentOptions{},
	})
	if err != nil {
		return nil, err
	}

	live := make([]int, 0, len(opts.PIDs))
	var missing []string
	for _, pid := range dedupePIDs(opts.PIDs) {
		if _, ok := snapshot.Get(pid); !ok {
			missing = append(missing, strconv.Itoa(pid))
			continue
		}
		live = append(live, pid)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no such process: %s", strings.Join(missing, ", "))
	}

	// Scoped to the named PIDs: resolving a working directory costs a syscall
	// per process, and every other process in the snapshot is irrelevant here.
	snapshot.PopulateWorkingDirectories(ctx, live)

	agents := make([]Agent, 0, len(live))
	for _, pid := range live {
		process, ok := snapshot.Get(pid)
		if !ok {
			continue
		}
		agents = append(agents, agentFrom(process, InspectedSource(process.Command)))
	}
	return agents, nil
}

// InspectedSource labels a process Source does not recognise as an agent, so an
// inspected PID still reports what it is: the agent source when known, else the
// executable name (agent-browser, node, zsh…).
func InspectedSource(command string) string {
	if source := Source(command); source != "" {
		return source
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(filepath.Base(fields[0]), `"'`)
}

func dedupePIDs(pids []int) []int {
	seen := make(map[int]bool, len(pids))
	unique := make([]int, 0, len(pids))
	for _, pid := range pids {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		unique = append(unique, pid)
	}
	return unique
}
