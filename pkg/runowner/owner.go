// Package runowner records which process drives a prompt run and judges
// whether it still does. A run is settled only by the process driving it, so a
// driver that exits first leaves the run unfinished forever, and with it the
// session it holds. The recorded owner is what makes "the driver is gone"
// provable instead of a guess about elapsed time.
package runowner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/process"
)

const (
	// HeartbeatInterval is how often a driver refreshes its claim, and
	// HeartbeatTTL how long a claim survives without one. Only an owner on
	// another host, or one whose process the OS will not describe, is judged
	// by its heartbeat: a local one is settled by asking the OS whether its
	// PID is still that process.
	HeartbeatInterval = 30 * time.Second
	HeartbeatTTL      = 3 * HeartbeatInterval

	// startSkew absorbs the difference between the start time the OS reports
	// (millisecond truncation, re-derived on every read) and the one stored at
	// claim time.
	startSkew = 2 * time.Second
)

var (
	localOnce  sync.Once
	localOwner database.PromptRunOwner
	localErr   error
)

// Local is this process's driver identity, resolved once. The token is minted
// per process so a recycled PID never reads as the same driver.
func Local() (database.PromptRunOwner, error) {
	localOnce.Do(func() {
		pid := os.Getpid()
		startedAt, err := processStartTime(pid)
		if err != nil {
			localErr = fmt.Errorf("runowner: resolve this process's start time: %w", err)
			return
		}
		localOwner = database.PromptRunOwner{
			HostID: database.LocalHostID(), PID: int64(pid), StartedAt: startedAt, Token: uuid.New(),
		}
	})
	return localOwner, localErr
}

// Claim records this process as the driver of run, keeps its heartbeat while
// the run is unfinished, and returns the claimed run. db may be a transaction:
// the heartbeat writes through the handle it was opened on, and stops by
// itself once the run is settled, reclaimed or claimed elsewhere, or was never
// committed.
func Claim(ctx context.Context, db *database.DB, runID uuid.UUID) (*database.PromptRun, error) {
	owner, err := Local()
	if err != nil {
		return nil, err
	}
	run, err := db.ClaimPromptRunOwner(ctx, runID, owner)
	if err != nil {
		return nil, err
	}
	heartbeats.start(db.Root(), runID, owner.Token)
	return run, nil
}

// Judge is the database.PromptRunJudge as of now: a run is driven while its
// owner's process is running, or, for an owner this host cannot inspect, while
// its heartbeat is fresh. A run admitted before ownership was recorded has no
// owner; it is judged by when it last changed, as a heartbeat it never sent.
func Judge(now time.Time) database.PromptRunJudge {
	return func(run database.PromptRun) (bool, string) {
		owner := run.Owner
		if owner == nil {
			return fresh(now, run.UpdatedAt, "no driver claimed the run; it last changed")
		}
		if owner.HostID != database.LocalHostID() {
			return heartbeatAlive(*owner, now)
		}
		switch processState(int(owner.PID)) {
		case processGone:
			return false, fmt.Sprintf("driver %s has exited", owner.Describe())
		case processUnknown:
			return heartbeatAlive(*owner, now)
		}
		startedAt, err := processStartTime(int(owner.PID))
		if err != nil {
			return heartbeatAlive(*owner, now)
		}
		if delta := startedAt.Sub(owner.StartedAt); delta > startSkew || delta < -startSkew {
			return false, fmt.Sprintf("driver %s has exited; the pid now belongs to a process started %s, not %s",
				owner.Describe(), startedAt.Format(time.RFC3339), owner.StartedAt.Format(time.RFC3339))
		}
		return true, fmt.Sprintf("driver %s is running", owner.Describe())
	}
}

func heartbeatAlive(owner database.PromptRunOwner, now time.Time) (bool, string) {
	if owner.HeartbeatAt == nil {
		return false, fmt.Sprintf("driver %s never recorded a heartbeat", owner.Describe())
	}
	return fresh(now, *owner.HeartbeatAt, fmt.Sprintf("driver %s last reported", owner.Describe()))
}

func fresh(now, at time.Time, subject string) (bool, string) {
	age := now.Sub(at).Round(time.Second)
	return age <= HeartbeatTTL, fmt.Sprintf("%s %s ago", subject, age)
}

type liveness int

const (
	processRunning liveness = iota
	processGone
	// processUnknown is a PID the OS will not let this process signal (another
	// user's, or anything outside a sandbox): it exists as far as anyone here
	// can tell, so only its heartbeat can settle it.
	processUnknown
)

func processState(pid int) liveness {
	if pid <= 0 {
		return processGone
	}
	handle, err := os.FindProcess(pid)
	if err != nil {
		return processGone
	}
	switch err := handle.Signal(syscall.Signal(0)); {
	case err == nil:
		return processRunning
	case errors.Is(err, os.ErrProcessDone):
		return processGone
	default:
		return processUnknown
	}
}

func processStartTime(pid int) (time.Time, error) {
	handle, err := process.NewProcess(int32(pid))
	if err != nil {
		return time.Time{}, err
	}
	createdMS, err := handle.CreateTime()
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(createdMS).UTC(), nil
}
