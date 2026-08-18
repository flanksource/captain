// Durable history for tasks dispatched to remote git-agents.
//
// Every write here is an idempotent upsert on a natural key, because the caller
// is a watcher that re-scans the same mailbox tree on every change and on a
// startup backfill. It must be able to replay the whole tree without producing
// duplicates or moving a task backwards.

package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// GitAgentTaskStatus is the lifecycle of one dispatched task. Only Dispatched
// and Running come from the protocol; the terminal states are derived, because
// the mailbox never records "this task is over".
type GitAgentTaskStatus string

const (
	GitAgentTaskDispatched GitAgentTaskStatus = "dispatched"
	GitAgentTaskRunning    GitAgentTaskStatus = "running"
	GitAgentTaskAccepted   GitAgentTaskStatus = "accepted"
	GitAgentTaskRejected   GitAgentTaskStatus = "rejected"
	GitAgentTaskErrored    GitAgentTaskStatus = "errored"
	GitAgentTaskTimedOut   GitAgentTaskStatus = "timed_out"
)

// GitAgentVerdictStatus mirrors gitagent.VerdictStatus.
type GitAgentVerdictStatus string

const (
	GitAgentVerdictAccepted GitAgentVerdictStatus = "accepted"
	GitAgentVerdictRejected GitAgentVerdictStatus = "rejected"
	GitAgentVerdictError    GitAgentVerdictStatus = "error"
)

type gitAgentTaskRecord struct {
	ID               uuid.UUID              `gorm:"column:id;type:uuid;primaryKey"`
	TaskID           string                 `gorm:"column:task_id"`
	Mailbox          string                 `gorm:"column:mailbox"`
	Repository       *string                `gorm:"column:repository"`
	Backend          *string                `gorm:"column:backend"`
	Agent            *string                `gorm:"column:agent"`
	PromptRunID      *uuid.UUID             `gorm:"column:prompt_run_id;type:uuid"`
	AdmissionKey     *string                `gorm:"column:admission_key"`
	Base             string                 `gorm:"column:base"`
	DispatchCommit   string                 `gorm:"column:dispatch_commit"`
	ControlCommit    *string                `gorm:"column:control_commit"`
	Relay            *string                `gorm:"column:relay"`
	Policy           []byte                 `gorm:"column:policy;type:jsonb"`
	Hooks            []byte                 `gorm:"column:hooks;type:jsonb"`
	Attempts         int                    `gorm:"column:attempts"`
	MaxAttempts      int                    `gorm:"column:max_attempts"`
	Status           GitAgentTaskStatus     `gorm:"column:status"`
	FinalStatus      *GitAgentVerdictStatus `gorm:"column:final_status"`
	IntegratedBranch *string                `gorm:"column:integrated_branch"`
	Error            *string                `gorm:"column:error"`
	DispatchedAt     time.Time              `gorm:"column:dispatched_at"`
	ConcludedAt      *time.Time             `gorm:"column:concluded_at"`
	CreatedAt        time.Time              `gorm:"column:created_at"`
	UpdatedAt        time.Time              `gorm:"column:updated_at"`
}

func (gitAgentTaskRecord) TableName() string { return "captain_git_agent_tasks" }

type gitAgentTaskAttemptRecord struct {
	ID              uuid.UUID             `gorm:"column:id;type:uuid;primaryKey"`
	TaskID          uuid.UUID             `gorm:"column:task_id;type:uuid"`
	Attempt         int                   `gorm:"column:attempt"`
	Tier            string                `gorm:"column:tier"`
	Status          GitAgentVerdictStatus `gorm:"column:status"`
	ProtocolVersion int                   `gorm:"column:protocol_version"`
	Findings        []byte                `gorm:"column:findings;type:jsonb"`
	ResultCommit    *string               `gorm:"column:result_commit"`
	Feedback        *string               `gorm:"column:feedback"`
	RecordedAt      time.Time             `gorm:"column:recorded_at"`
	CreatedAt       time.Time             `gorm:"column:created_at"`
}

func (gitAgentTaskAttemptRecord) TableName() string { return "captain_git_agent_task_attempts" }

// GitAgentTask is one dispatched task as the API serves it.
type GitAgentTask struct {
	ID               uuid.UUID              `json:"id"`
	TaskID           string                 `json:"taskId"`
	Mailbox          string                 `json:"mailbox"`
	Repository       string                 `json:"repository,omitempty"`
	Backend          string                 `json:"backend,omitempty"`
	Agent            string                 `json:"agent,omitempty"`
	PromptRunID      *uuid.UUID             `json:"promptRunId,omitempty"`
	Base             string                 `json:"base"`
	DispatchCommit   string                 `json:"dispatchCommit"`
	Relay            string                 `json:"relay,omitempty"`
	Policy           map[string]any         `json:"policy,omitempty"`
	Attempts         int                    `json:"attempts"`
	MaxAttempts      int                    `json:"maxAttempts,omitempty"`
	Status           GitAgentTaskStatus     `json:"status"`
	FinalStatus      *GitAgentVerdictStatus `json:"finalStatus,omitempty"`
	IntegratedBranch string                 `json:"integratedBranch,omitempty"`
	Error            string                 `json:"error,omitempty"`
	DispatchedAt     time.Time              `json:"dispatchedAt"`
	ConcludedAt      *time.Time             `json:"concludedAt,omitempty"`
	UpdatedAt        time.Time              `json:"updatedAt"`
}

// GitAgentTaskAttempt is one tier's verdict on one attempt.
type GitAgentTaskAttempt struct {
	Attempt      int                   `json:"attempt"`
	Tier         string                `json:"tier"`
	Status       GitAgentVerdictStatus `json:"status"`
	Findings     []map[string]any      `json:"findings,omitempty"`
	ResultCommit string                `json:"resultCommit,omitempty"`
	Feedback     string                `json:"feedback,omitempty"`
	RecordedAt   time.Time             `json:"recordedAt"`
}

// GitAgentTaskDetail is a task with its verdicts, oldest attempt first.
type GitAgentTaskDetail struct {
	Task     GitAgentTask          `json:"task"`
	Attempts []GitAgentTaskAttempt `json:"attempts"`
}

// UpsertGitAgentTaskInput is what one scan of a mailbox task directory yields.
type UpsertGitAgentTaskInput struct {
	TaskID         string
	Mailbox        string
	Repository     string
	Backend        string
	Agent          string
	AdmissionKey   string
	Base           string
	DispatchCommit string
	ControlCommit  string
	Relay          string
	Policy         map[string]any
	Hooks          map[string]any
	Attempts       int
	MaxAttempts    int
	Status         GitAgentTaskStatus
	DispatchedAt   time.Time
}

// UpsertGitAgentTask records or refreshes one task, keyed on (mailbox, task_id).
//
// `attempts` is raised with GREATEST rather than assigned: scans can arrive out
// of order (fsnotify coalesces, and the backfill walks the whole tree), and a
// stale scan must not walk the count backwards. `status` is likewise only
// advanced out of the non-terminal states — a re-scan after a task concluded
// must not reset it to "running".
func (db *DB) UpsertGitAgentTask(ctx context.Context, input UpsertGitAgentTaskInput) (uuid.UUID, error) {
	if err := db.requireGorm(); err != nil {
		return uuid.Nil, err
	}
	policy, err := marshalJSONColumn(input.Policy, "{}")
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode git-agent policy: %w", err)
	}
	hooks, err := marshalJSONColumn(input.Hooks, "")
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode git-agent hooks: %w", err)
	}
	status := input.Status
	if status == "" {
		status = GitAgentTaskDispatched
	}
	dispatchedAt := input.DispatchedAt
	if dispatchedAt.IsZero() {
		dispatchedAt = time.Now().UTC()
	}
	record := gitAgentTaskRecord{
		ID:             uuid.New(),
		TaskID:         input.TaskID,
		Mailbox:        input.Mailbox,
		Repository:     nullableTrimmed(input.Repository),
		Backend:        nullableTrimmed(input.Backend),
		Agent:          nullableTrimmed(input.Agent),
		AdmissionKey:   nullableTrimmed(input.AdmissionKey),
		Base:           input.Base,
		DispatchCommit: input.DispatchCommit,
		ControlCommit:  nullableTrimmed(input.ControlCommit),
		Relay:          nullableTrimmed(input.Relay),
		Policy:         policy,
		Hooks:          hooks,
		Attempts:       input.Attempts,
		MaxAttempts:    input.MaxAttempts,
		Status:         status,
		DispatchedAt:   dispatchedAt,
	}
	err = db.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "mailbox"}, {Name: "task_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"repository":     gorm0Coalesce("repository"),
			"backend":        gorm0Coalesce("backend"),
			"agent":          gorm0Coalesce("agent"),
			"admission_key":  gorm0Coalesce("admission_key"),
			"control_commit": gorm0Coalesce("control_commit"),
			"relay":          gorm0Coalesce("relay"),
			"policy":         clause.Column{Table: "excluded", Name: "policy"},
			"hooks":          gorm0Coalesce("hooks"),
			"attempts":       clause.Expr{SQL: "GREATEST(captain_git_agent_tasks.attempts, excluded.attempts)"},
			"max_attempts":   clause.Expr{SQL: "GREATEST(captain_git_agent_tasks.max_attempts, excluded.max_attempts)"},
			"status":         clause.Expr{SQL: gitAgentStatusAdvance},
			"updated_at":     clause.Expr{SQL: "now()"},
		}),
	}).Create(&record).Error
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert git-agent task: %w", err)
	}
	// Create returns the generated id only on insert; on conflict the row keeps
	// its original id, so read it back rather than trusting the struct. Selected
	// into the record type so gorm applies its uuid mapping — Pluck into a
	// uuid.UUID would try to scan the text form into a [16]byte.
	var existing gitAgentTaskRecord
	if err := db.gorm.WithContext(ctx).Select("id").
		Where("mailbox = ? AND task_id = ?", input.Mailbox, input.TaskID).
		Take(&existing).Error; err != nil {
		return uuid.Nil, fmt.Errorf("read git-agent task id: %w", err)
	}
	return existing.ID, nil
}

// gitAgentStatusAdvance keeps a concluded task concluded. Once a terminal state
// is recorded, a later scan of the same directory (which cannot tell that the
// task finished) must not drag it back to dispatched/running.
const gitAgentStatusAdvance = `CASE
	WHEN captain_git_agent_tasks.status IN ('accepted','rejected','errored','timed_out')
		THEN captain_git_agent_tasks.status
	ELSE excluded.status
END`

// gorm0Coalesce keeps a previously-recorded value when the incoming scan has
// none. A partial scan (a task directory read mid-write) must not blank a field
// that an earlier, more complete scan already established.
func gorm0Coalesce(column string) clause.Expr {
	return clause.Expr{SQL: fmt.Sprintf("COALESCE(excluded.%s, captain_git_agent_tasks.%s)", column, column)}
}

// RecordGitAgentAttemptInput is one verdict.json.
type RecordGitAgentAttemptInput struct {
	TaskID          uuid.UUID
	Attempt         int
	Tier            string
	Status          GitAgentVerdictStatus
	ProtocolVersion int
	Findings        []map[string]any
	ResultCommit    string
	Feedback        string
	RecordedAt      time.Time
}

// RecordGitAgentAttempt stores one tier's verdict, keyed on
// (task, attempt, tier) so re-scanning the verdicts directory is a no-op.
func (db *DB) RecordGitAgentAttempt(ctx context.Context, input RecordGitAgentAttemptInput) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	findings, err := marshalJSONColumn(input.Findings, "[]")
	if err != nil {
		return fmt.Errorf("encode git-agent findings: %w", err)
	}
	recordedAt := input.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = time.Now().UTC()
	}
	version := input.ProtocolVersion
	if version == 0 {
		version = 1
	}
	record := gitAgentTaskAttemptRecord{
		ID:              uuid.New(),
		TaskID:          input.TaskID,
		Attempt:         input.Attempt,
		Tier:            input.Tier,
		Status:          input.Status,
		ProtocolVersion: version,
		Findings:        findings,
		ResultCommit:    nullableTrimmed(input.ResultCommit),
		Feedback:        nullableTrimmed(input.Feedback),
		RecordedAt:      recordedAt,
	}
	err = db.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "task_id"}, {Name: "attempt"}, {Name: "tier"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"status", "protocol_version", "findings", "result_commit", "feedback", "recorded_at",
		}),
	}).Create(&record).Error
	if err != nil {
		return fmt.Errorf("record git-agent attempt: %w", err)
	}
	return nil
}

// ConcludeGitAgentTask records the terminal state once a verdict decides the
// task. Separate from the upsert because the watcher derives it from the
// verdict set rather than reading it from any single file.
func (db *DB) ConcludeGitAgentTask(ctx context.Context, id uuid.UUID,
	status GitAgentTaskStatus, verdict GitAgentVerdictStatus, integratedBranch string, when time.Time,
) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	if when.IsZero() {
		when = time.Now().UTC()
	}
	updates := map[string]any{
		"status":       status,
		"final_status": verdict,
		"concluded_at": when,
		"updated_at":   clause.Expr{SQL: "now()"},
	}
	if branch := nullableTrimmed(integratedBranch); branch != nil {
		updates["integrated_branch"] = *branch
	}
	if err := db.gorm.WithContext(ctx).Model(&gitAgentTaskRecord{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("conclude git-agent task: %w", err)
	}
	return nil
}

// LinkGitAgentTasksToPromptRuns fills prompt_run_id for tasks whose admission
// key now matches a persisted prompt run.
//
// The link cannot be written at dispatch: persistPromptRun creates the
// captain_prompt_runs row only after the run finishes, by which time the remote
// task has already concluded. So the task always lands first and the association
// is resolved on a later pass.
func (db *DB) LinkGitAgentTasksToPromptRuns(ctx context.Context) (int64, error) {
	if err := db.requireGorm(); err != nil {
		return 0, err
	}
	result := db.gorm.WithContext(ctx).Exec(`
		UPDATE captain_git_agent_tasks t
		SET prompt_run_id = r.id, updated_at = now()
		FROM captain_prompt_runs r
		WHERE t.prompt_run_id IS NULL
			AND t.admission_key IS NOT NULL
			AND r.admission_key = t.admission_key`)
	if result.Error != nil {
		return 0, fmt.Errorf("link git-agent tasks to prompt runs: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// ListGitAgentTasksFilter narrows the history list.
type ListGitAgentTasksFilter struct {
	Backend string
	Agent   string
	Status  GitAgentTaskStatus
	Limit   int
}

// ListGitAgentTasks returns task history newest first. It always returns a
// slice so a consumer can iterate it unconditionally.
func (db *DB) ListGitAgentTasks(ctx context.Context, filter ListGitAgentTasksFilter) ([]GitAgentTask, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	query := db.gorm.WithContext(ctx).Model(&gitAgentTaskRecord{})
	if backend := nullableTrimmed(filter.Backend); backend != nil {
		query = query.Where("backend = ?", *backend)
	}
	if agent := nullableTrimmed(filter.Agent); agent != nil {
		query = query.Where("agent = ?", *agent)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var records []gitAgentTaskRecord
	if err := query.Order("dispatched_at DESC, id DESC").Limit(limit).
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list git-agent tasks: %w", err)
	}
	tasks := make([]GitAgentTask, 0, len(records))
	for _, record := range records {
		tasks = append(tasks, record.toTask())
	}
	return tasks, nil
}

// GetGitAgentTask reads one task with its verdicts. ok is false when unknown.
func (db *DB) GetGitAgentTask(ctx context.Context, mailbox, taskID string) (*GitAgentTaskDetail, bool, error) {
	if err := db.requireGorm(); err != nil {
		return nil, false, err
	}
	var records []gitAgentTaskRecord
	query := db.gorm.WithContext(ctx).Model(&gitAgentTaskRecord{}).Where("task_id = ?", taskID)
	if trimmed := nullableTrimmed(mailbox); trimmed != nil {
		query = query.Where("mailbox = ?", *trimmed)
	}
	if err := query.Order("dispatched_at DESC").Limit(2).Find(&records).Error; err != nil {
		return nil, false, fmt.Errorf("read git-agent task: %w", err)
	}
	if len(records) == 0 {
		return nil, false, nil
	}
	// A task id is unique only within its mailbox, so an unscoped lookup that
	// matches more than one row is ambiguous and must say so rather than
	// silently returning whichever sorted first.
	if len(records) > 1 {
		return nil, false, fmt.Errorf("task %q exists in more than one mailbox; pass a mailbox to disambiguate", taskID)
	}
	var attempts []gitAgentTaskAttemptRecord
	if err := db.gorm.WithContext(ctx).Model(&gitAgentTaskAttemptRecord{}).
		Where("task_id = ?", records[0].ID).
		Order("attempt ASC, tier ASC").Find(&attempts).Error; err != nil {
		return nil, false, fmt.Errorf("read git-agent task attempts: %w", err)
	}
	detail := GitAgentTaskDetail{Task: records[0].toTask(), Attempts: make([]GitAgentTaskAttempt, 0, len(attempts))}
	for _, attempt := range attempts {
		detail.Attempts = append(detail.Attempts, attempt.toAttempt())
	}
	return &detail, true, nil
}

func (r gitAgentTaskRecord) toTask() GitAgentTask {
	task := GitAgentTask{
		ID:               r.ID,
		TaskID:           r.TaskID,
		Mailbox:          r.Mailbox,
		Repository:       derefString(r.Repository),
		Backend:          derefString(r.Backend),
		Agent:            derefString(r.Agent),
		PromptRunID:      r.PromptRunID,
		Base:             r.Base,
		DispatchCommit:   r.DispatchCommit,
		Relay:            derefString(r.Relay),
		Attempts:         r.Attempts,
		MaxAttempts:      r.MaxAttempts,
		Status:           r.Status,
		FinalStatus:      r.FinalStatus,
		IntegratedBranch: derefString(r.IntegratedBranch),
		Error:            derefString(r.Error),
		DispatchedAt:     r.DispatchedAt,
		ConcludedAt:      r.ConcludedAt,
		UpdatedAt:        r.UpdatedAt,
	}
	if len(r.Policy) > 0 {
		_ = json.Unmarshal(r.Policy, &task.Policy)
	}
	return task
}

func (r gitAgentTaskAttemptRecord) toAttempt() GitAgentTaskAttempt {
	attempt := GitAgentTaskAttempt{
		Attempt:      r.Attempt,
		Tier:         r.Tier,
		Status:       r.Status,
		ResultCommit: derefString(r.ResultCommit),
		Feedback:     derefString(r.Feedback),
		RecordedAt:   r.RecordedAt,
	}
	if len(r.Findings) > 0 {
		_ = json.Unmarshal(r.Findings, &attempt.Findings)
	}
	return attempt
}

// marshalJSONColumn encodes a jsonb column, returning nil for an empty value
// when fallback is empty so the column stays NULL rather than storing "null".
func marshalJSONColumn(value any, fallback string) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		if fallback == "" {
			return nil, nil
		}
		return []byte(fallback), nil
	case map[string]any:
		if len(typed) == 0 {
			if fallback == "" {
				return nil, nil
			}
			return []byte(fallback), nil
		}
	case []map[string]any:
		if len(typed) == 0 {
			if fallback == "" {
				return nil, nil
			}
			return []byte(fallback), nil
		}
	}
	return json.Marshal(value)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
