package cli

import (
	"context"
	"fmt"
	"os"

	clickyprocess "github.com/flanksource/clicky/process"
)

type currentLiveRuntime struct {
	getenv func(string) string
	// ancestors are this process's ancestor PIDs, nearest first.
	ancestors func(ctx context.Context) ([]int, error)
	live      func(ctx context.Context) ([]SessionRecord, error)
}

// CurrentLiveSession is the live agent session this process runs inside, as
// RunSessionLive lists it: the nearest ancestor process that is a live
// session, else the live session the environment's session marker names. It
// is nil when the process runs inside no agent, and an error when a marker
// names a session that is not live.
func CurrentLiveSession(ctx context.Context) (*SessionRecord, error) {
	return currentLiveSession(ctx, currentLiveRuntime{
		getenv: os.Getenv,
		ancestors: func(ctx context.Context) ([]int, error) {
			snapshot, err := clickyprocess.Discover(ctx, clickyprocess.SnapshotOptions{})
			if err != nil {
				return nil, err
			}
			var pids []int
			for _, process := range snapshot.Ancestors(os.Getpid()) {
				pids = append(pids, process.PID)
			}
			return pids, nil
		},
		live: func(ctx context.Context) ([]SessionRecord, error) {
			result, err := RunSessionLive(ctx, SessionLiveOptions{Source: "all", All: true, Full: true})
			return result.Sessions, err
		},
	})
}

func currentLiveSession(ctx context.Context, runtime currentLiveRuntime) (*SessionRecord, error) {
	marker := detectEnvironmentSession(runtime.getenv)
	if marker == nil {
		return nil, nil
	}
	records, err := runtime.live(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing live sessions: %w", err)
	}
	ancestors, err := runtime.ancestors(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing this process's ancestors: %w", err)
	}
	byPID := map[int]SessionRecord{}
	for _, record := range records {
		if record.Live != nil && record.Live.PID > 0 {
			byPID[record.Live.PID] = record
		}
	}
	for _, pid := range ancestors {
		if record, ok := byPID[pid]; ok {
			return &record, nil
		}
	}
	for _, record := range records {
		if record.Live != nil && marker.SessionID != "" && record.ID == marker.SessionID {
			return &record, nil
		}
	}
	if marker.SessionID == "" {
		return nil, fmt.Errorf("%s marks a %s session, but no ancestor of this process is a live session", marker.Marker, marker.Source)
	}
	return nil, fmt.Errorf("%s=%s names no live %s session", marker.Marker, marker.SessionID, marker.Source)
}
