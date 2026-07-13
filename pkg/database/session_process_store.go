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

var ErrInvalidSessionProcess = errors.New("invalid Captain session process")

// SessionProcessInput is one live-process observation. The identity key is
// (HostID, BootID, PID, ProcessStartedAt); repeated polls converge on one row.
type SessionProcessInput struct {
	SessionID        uuid.UUID
	HostID           string
	BootID           string
	PID              int64
	ProcessStartedAt time.Time
	Status           string
	Command          string
	CWD              string
	Source           string
	CPUPercent       float64
	MemoryPercent    float64
	MemoryRSSBytes   *int64
	SampledAt        time.Time
}

type sessionProcessRecord struct {
	ID               uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	SessionID        uuid.UUID  `gorm:"column:session_id;type:uuid"`
	HostID           string     `gorm:"column:host_id"`
	BootID           string     `gorm:"column:boot_id"`
	PID              int64      `gorm:"column:pid"`
	ProcessStartedAt time.Time  `gorm:"column:process_started_at"`
	Status           string     `gorm:"column:status"`
	Command          *string    `gorm:"column:command"`
	CWD              *string    `gorm:"column:cwd"`
	Source           string     `gorm:"column:source"`
	CPUPercent       float64    `gorm:"column:cpu_percent"`
	MemoryPercent    float64    `gorm:"column:memory_percent"`
	MemoryRSSBytes   *int64     `gorm:"column:memory_rss_bytes"`
	SampledAt        *time.Time `gorm:"column:sampled_at"`
	LastHeartbeatAt  *time.Time `gorm:"column:last_heartbeat_at"`
	EndedAt          *time.Time `gorm:"column:ended_at"`
}

func (sessionProcessRecord) TableName() string { return "captain_session_processes" }

// UpsertSessionProcess records a live-process sample, creating the process row
// on first sight and refreshing metrics/status/heartbeat afterwards.
func (db *DB) UpsertSessionProcess(ctx context.Context, input SessionProcessInput) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	if input.SessionID == uuid.Nil {
		return fmt.Errorf("%w: session ID is required", ErrInvalidSessionProcess)
	}
	if input.PID <= 0 || input.ProcessStartedAt.IsZero() {
		return fmt.Errorf("%w: PID and process start time are required", ErrInvalidSessionProcess)
	}
	hostID := strings.TrimSpace(input.HostID)
	if hostID == "" {
		hostID = "local"
	}
	bootID := strings.TrimSpace(input.BootID)
	if bootID == "" {
		bootID = "unknown"
	}
	sampledAt := input.SampledAt
	if sampledAt.IsZero() {
		sampledAt = time.Now().UTC()
	}
	record := sessionProcessRecord{
		ID: uuid.New(), SessionID: input.SessionID, HostID: hostID, BootID: bootID,
		PID: input.PID, ProcessStartedAt: input.ProcessStartedAt.UTC(),
		Status: strings.TrimSpace(input.Status), Command: nullableTrimmed(input.Command),
		CWD: nullableTrimmed(input.CWD), Source: strings.TrimSpace(input.Source),
		CPUPercent: input.CPUPercent, MemoryPercent: input.MemoryPercent,
		MemoryRSSBytes: input.MemoryRSSBytes, SampledAt: &sampledAt, LastHeartbeatAt: &sampledAt,
	}
	if record.Status == "" {
		record.Status = "running"
	}
	err := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "host_id"}, {Name: "boot_id"}, {Name: "pid"}, {Name: "process_started_at"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"session_id", "status", "command", "cwd", "source", "cpu_percent", "memory_percent",
			"memory_rss_bytes", "sampled_at", "last_heartbeat_at",
		}),
	}).Create(&record).Error
	if err != nil {
		return fmt.Errorf("upsert Captain session process pid %d: %w", input.PID, err)
	}
	return nil
}

// FindProcessSessionID resolves the session a process identity was previously
// bound to, so monitor restarts reuse provisional sessions instead of minting
// new ones. Returns uuid.Nil when the process has never been recorded.
func (db *DB) FindProcessSessionID(ctx context.Context, hostID, bootID string, pid int64, startedAt time.Time) (uuid.UUID, error) {
	if err := db.requireGorm(); err != nil {
		return uuid.Nil, err
	}
	var record sessionProcessRecord
	err := db.gorm.WithContext(ctx).
		Where("host_id = ? AND boot_id = ? AND pid = ? AND process_started_at = ?",
			hostID, bootID, pid, startedAt.UTC()).
		First(&record).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, nil
		}
		return uuid.Nil, fmt.Errorf("find Captain session process: %w", err)
	}
	return record.SessionID, nil
}

// EndOtherSessionProcesses closes open process rows for a session except the
// given PID, satisfying the one-active-process-per-session constraint before a
// new process binds to the session (e.g. resumed under a new PID).
func (db *DB) EndOtherSessionProcesses(ctx context.Context, sessionID uuid.UUID, keepPID int64) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	err := db.gorm.WithContext(ctx).Model(&sessionProcessRecord{}).
		Where("session_id = ? AND ended_at IS NULL AND pid <> ?", sessionID, keepPID).
		Updates(map[string]any{"ended_at": time.Now().UTC(), "status": "exited"}).Error
	if err != nil {
		return fmt.Errorf("end superseded Captain session processes: %w", err)
	}
	return nil
}

// FindSessionIDByCWD resolves the most recently active session for a working
// directory and source — the process-to-session heuristic when the command
// line carries no session id.
func (db *DB) FindSessionIDByCWD(ctx context.Context, source, cwd string) (uuid.UUID, error) {
	if err := db.requireGorm(); err != nil {
		return uuid.Nil, err
	}
	cwd = strings.TrimRight(strings.TrimSpace(cwd), "/")
	if cwd == "" {
		return uuid.Nil, nil
	}
	var record sessionRecord
	err := db.gorm.WithContext(ctx).
		Where("source = ? AND parent_session_id IS NULL AND rtrim(cwd, '/') = ?", source, cwd).
		Order("COALESCE(last_activity_at, started_at, created_at) DESC").
		First(&record).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, nil
		}
		return uuid.Nil, fmt.Errorf("find Captain session by cwd: %w", err)
	}
	return record.ID, nil
}

// EndVanishedProcesses marks every still-open process row on the host whose PID
// is absent from alivePIDs as ended, and returns how many rows were closed.
func (db *DB) EndVanishedProcesses(ctx context.Context, hostID string, alivePIDs []int64) (int64, error) {
	if err := db.requireGorm(); err != nil {
		return 0, err
	}
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		hostID = "local"
	}
	now := time.Now().UTC()
	query := db.gorm.WithContext(ctx).Model(&sessionProcessRecord{}).
		Where("host_id = ? AND ended_at IS NULL", hostID)
	if len(alivePIDs) > 0 {
		query = query.Where("pid NOT IN ?", alivePIDs)
	}
	result := query.Updates(map[string]any{"ended_at": now, "status": "exited"})
	if result.Error != nil {
		return 0, fmt.Errorf("end vanished Captain session processes: %w", result.Error)
	}
	return result.RowsAffected, nil
}
