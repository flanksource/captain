package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PromptRunOwner is the process driving a prompt run to completion. Host plus
// PID plus the process start time identify it: PIDs are recycled, start times
// are not. Token tells two claims by processes sharing the first three apart,
// and HeartbeatAt is the evidence another host judges the owner by.
type PromptRunOwner struct {
	HostID      string     `json:"hostId"`
	PID         int64      `json:"pid"`
	StartedAt   time.Time  `json:"startedAt"`
	Token       uuid.UUID  `json:"token"`
	HeartbeatAt *time.Time `json:"heartbeatAt,omitempty"`
}

// Describe renders the owner for an operator-facing message.
func (o PromptRunOwner) Describe() string {
	return fmt.Sprintf("%s pid %d", o.HostID, o.PID)
}

// drivenPromptRunStates are the states a run's driver is still meant to be
// settling. A waiting run is parked on an answer, not on a process, so nothing
// reclaims it.
var drivenPromptRunStates = []PromptRunState{PromptRunStatePending, PromptRunStateRunning}

// claimablePromptRunStates are the states a driver may hold a run in.
var claimablePromptRunStates = []PromptRunState{PromptRunStatePending, PromptRunStateRunning, PromptRunStateWaiting}

func promptRunOwnerFromRecord(record promptRunRecord) *PromptRunOwner {
	if record.OwnerHostID == nil || record.OwnerPID == nil || record.OwnerStartedAt == nil || record.OwnerToken == nil {
		return nil
	}
	return &PromptRunOwner{
		HostID: *record.OwnerHostID, PID: *record.OwnerPID, StartedAt: record.OwnerStartedAt.UTC(),
		Token: *record.OwnerToken, HeartbeatAt: record.OwnerHeartbeatAt,
	}
}

// ErrPromptRunDriven is a claim refused because another process still drives
// the run.
var ErrPromptRunDriven = errors.New("captain prompt run is driven by another process")

// ClaimPromptRunOwner records owner as the driver of an unfinished run, with a
// fresh heartbeat, and returns the claimed run. A claim replaces the one before
// it, unless refuseIfDriven is set and judges the previous owner, another
// process, still driving the run: that claim fails with ErrPromptRunDriven
// instead of orphaning a driver that would keep executing the run. A nil
// refuseIfDriven takes the run over unconditionally.
func (db *DB) ClaimPromptRunOwner(ctx context.Context, id uuid.UUID, owner PromptRunOwner, refuseIfDriven PromptRunJudge) (*PromptRun, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, fmt.Errorf("%w: prompt run ID is required", ErrInvalidPromptRun)
	}
	if strings.TrimSpace(owner.HostID) == "" || owner.PID <= 0 || owner.StartedAt.IsZero() || owner.Token == uuid.Nil {
		return nil, fmt.Errorf("%w: a prompt run owner needs a host, PID, start time and token", ErrInvalidPromptRun)
	}
	var claimed *PromptRun
	err := db.Transaction(ctx, func(tx *DB) error {
		var record promptRunRecord
		lookup := tx.gorm.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND state IN ?", id, claimablePromptRunStates).Limit(1).Find(&record)
		if lookup.Error != nil {
			return fmt.Errorf("lock Captain prompt run %s: %w", id, lookup.Error)
		}
		if lookup.RowsAffected == 0 {
			return fmt.Errorf("%w: prompt run %s does not exist or already finished", ErrPromptRunNotFound, id)
		}
		if previous := promptRunOwnerFromRecord(record); refuseIfDriven != nil && previous != nil && previous.Token != owner.Token {
			if alive, reason := refuseIfDriven(promptRunFromRecord(record)); alive {
				return fmt.Errorf("%w: %w: prompt run %s: %s", ErrPromptRunConflict, ErrPromptRunDriven, id, reason)
			}
		}
		if err := tx.gorm.WithContext(ctx).Model(&promptRunRecord{}).Where("id = ?", id).
			Updates(map[string]any{
				"owner_host_id": strings.TrimSpace(owner.HostID), "owner_pid": owner.PID,
				"owner_started_at": owner.StartedAt.UTC(), "owner_token": owner.Token,
				"owner_heartbeat_at": gorm.Expr("clock_timestamp()"),
			}).Error; err != nil {
			return fmt.Errorf("claim Captain prompt run %s: %w", id, err)
		}
		var err error
		claimed, err = tx.GetPromptRun(ctx, id)
		return err
	})
	return claimed, err
}

// TouchPromptRunOwner refreshes the heartbeat of the claim token holds. It
// reports false, touching nothing, once the run finished or another process
// claimed or reclaimed it: the holder is no longer its driver.
func (db *DB) TouchPromptRunOwner(ctx context.Context, id, token uuid.UUID) (bool, error) {
	if err := db.requireGorm(); err != nil {
		return false, err
	}
	result := db.gorm.WithContext(ctx).Model(&promptRunRecord{}).
		Where("id = ? AND owner_token = ? AND state IN ?", id, token, claimablePromptRunStates).
		Update("owner_heartbeat_at", gorm.Expr("clock_timestamp()"))
	if result.Error != nil {
		return false, fmt.Errorf("refresh Captain prompt run %s owner: %w", id, result.Error)
	}
	return result.RowsAffected == 1, nil
}

// ListDrivenPromptRuns returns the pending and running runs, oldest first: the
// runs a driver is meant to be settling, which the orphan sweep judges.
func (db *DB) ListDrivenPromptRuns(ctx context.Context) ([]PromptRun, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var records []promptRunRecord
	if err := db.gorm.WithContext(ctx).Where("state IN ?", drivenPromptRunStates).
		Order("queued_at, id").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list driven Captain prompt runs: %w", err)
	}
	runs := make([]PromptRun, len(records))
	for i := range records {
		runs[i] = promptRunFromRecord(records[i])
	}
	return runs, nil
}

// PromptRunJudge decides whether anything is still driving run, and why.
type PromptRunJudge func(run PromptRun) (alive bool, reason string)

// ReclaimPromptRun cancels a pending or running run that judge finds nothing is
// driving, with "reclaimed: <reason>" as its error, and cancels its pending
// turn requests with it: left pending, a dashboard would offer to answer them
// and an answer would unblock a broker no longer there. It returns the
// reclaimed run, or nil when the run is not reclaimable: settled, waiting on an
// answer, or still driven.
//
// The run is judged under a row lock, so a driver's settle, a fresh claim and a
// second reclaimer all serialise against the judgement instead of racing it.
func (db *DB) ReclaimPromptRun(ctx context.Context, id uuid.UUID, judge PromptRunJudge) (*PromptRun, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if judge == nil {
		return nil, errors.New("reclaiming a prompt run requires a judge")
	}
	var reclaimed *PromptRun
	err := db.Transaction(ctx, func(tx *DB) error {
		var record promptRunRecord
		if err := tx.gorm.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&record, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %s", ErrPromptRunNotFound, id)
			}
			return fmt.Errorf("lock Captain prompt run %s: %w", id, err)
		}
		if record.State != PromptRunStatePending && record.State != PromptRunStateRunning {
			return nil
		}
		run := promptRunFromRecord(record)
		alive, reason := judge(run)
		if alive {
			return nil
		}
		state, phase, message := PromptRunStateCancelled, run.Phase, "reclaimed: "+reason
		if phase == PromptRunPhaseQueued || phase == PromptRunPhasePreRun {
			phase = PromptRunPhaseGenerate
		}
		updated, err := tx.UpdatePromptRun(ctx, UpdatePromptRunInput{
			ID: run.ID, ExpectedVersion: run.Version, State: &state, Phase: &phase, Error: &message,
		})
		if err != nil {
			return fmt.Errorf("reclaim Captain prompt run %s: %w", id, err)
		}
		if err := tx.CancelPendingTurnRequests(ctx, run.SessionID, run.ID, message); err != nil {
			return fmt.Errorf("cancel pending requests of reclaimed prompt run %s: %w", id, err)
		}
		reclaimed = updated
		return nil
	})
	return reclaimed, err
}
