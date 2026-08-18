// Ingest of remote git-agent task history into Postgres.
//
// This runs on the supervisor, in captain serve, alongside the transcript
// backfill — and only there, for two reasons. Every supervisor-side write to a
// task tree happens under one deterministic root (the mailbox), and serve is
// already the single DB writer holding the monitor's advisory lock. The agent
// host runs `git-agent serve` with no database at all, so a watcher that finds
// no mailbox root simply does nothing.
//
// It is a watcher rather than a write-through from the dispatch adapter because
// AwaitOutcome returns only the *final* verdict: every intermediate rejected
// attempt, and the relay error path, would be invisible to a write-through. It
// also means a plain `captain ai prompt` run, which has no DB handle, still
// gets its history recorded by whatever serve is running.

package monitor

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent"
)

// ingestGitAgentTasks records every task found under every configured
// git-agent backend's mailbox root. It is idempotent: the store upserts on
// natural keys, so re-running it over unchanged state changes nothing.
func (m *Monitor) ingestGitAgentTasks(ctx context.Context) {
	for _, root := range gitAgentMailboxRoots() {
		mailboxes, err := gitagent.ScanMailboxes(root.servedRoot)
		if err != nil {
			log.Warnf("git-agent ingest: scan %s: %v", root.servedRoot, err)
			continue
		}
		for _, mailbox := range mailboxes {
			if err := m.ingestMailbox(ctx, root.backend, mailbox); err != nil {
				log.Warnf("git-agent ingest: mailbox %s: %v", mailbox.Route, err)
			}
			if ctx.Err() != nil {
				return
			}
		}
	}
	// Fill in prompt-run links that could not exist when the task was recorded:
	// the run row is written only after the run finishes.
	if linked, err := m.db.LinkGitAgentTasksToPromptRuns(ctx); err != nil {
		log.Warnf("git-agent ingest: link prompt runs: %v", err)
	} else if linked > 0 {
		log.Infof("git-agent ingest: linked %d task(s) to their prompt run", linked)
	}
}

type gitAgentRoot struct {
	backend    string
	servedRoot string
}

// gitAgentMailboxRoots resolves the served root of every configured git-agent
// backend. A malformed config is logged and skipped rather than aborting the
// pass: one bad backend must not stop history for the others.
func gitAgentMailboxRoots() []gitAgentRoot {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		log.Warnf("git-agent ingest: load config: %v", err)
		return nil
	}
	roots := make([]gitAgentRoot, 0, len(cfg.Sandbox.Backends))
	seen := map[string]bool{}
	for name, backend := range cfg.Sandbox.Backends {
		kind, ok := api.ParseSandboxKind(backend.Kind)
		if !ok || kind != api.SandboxGitAgent {
			continue
		}
		servedRoot, err := gitagent.ServedRootFor(backend.Options)
		if err != nil {
			log.Warnf("git-agent ingest: backend %s: %v", name, err)
			continue
		}
		// Two backends may share a root; scanning it twice would be harmless but
		// wasteful, and would attribute the same task to both.
		if seen[servedRoot] {
			continue
		}
		seen[servedRoot] = true
		roots = append(roots, gitAgentRoot{backend: name, servedRoot: servedRoot})
	}
	return roots
}

func (m *Monitor) ingestMailbox(ctx context.Context, backend string, mailbox gitagent.Mailbox) error {
	snapshots, err := gitagent.ScanTasks(mailbox.Path)
	if err != nil {
		return err
	}
	for _, snapshot := range snapshots {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := m.ingestTask(ctx, backend, mailbox, snapshot); err != nil {
			log.Warnf("git-agent ingest: task %s: %v", snapshot.Task, err)
		}
	}
	return nil
}

func (m *Monitor) ingestTask(ctx context.Context, backend string,
	mailbox gitagent.Mailbox, snapshot gitagent.TaskSnapshot,
) error {
	input := database.UpsertGitAgentTaskInput{
		TaskID:     snapshot.Task,
		Mailbox:    mailbox.Route,
		Repository: mailbox.Repository,
		Backend:    backend,
		Status:     gitAgentLiveStatus(snapshot),
	}
	if state := snapshot.State; state != nil {
		input.Agent = state.Agent
		input.Base = state.Base
		input.DispatchCommit = state.DispatchCommit
		input.ControlCommit = state.ControlCommit
		input.Relay = string(state.Relay)
		input.Attempts = state.Attempts
		input.MaxAttempts = state.Policy.MaxAttempts
		input.DispatchedAt = state.UpdatedAt
		input.Policy = map[string]any{
			"paths":       state.Policy.Paths,
			"maxAttempts": state.Policy.MaxAttempts,
			"maxBlobSize": state.Policy.MaxBlobSize,
		}
	}
	id, err := m.db.UpsertGitAgentTask(ctx, input)
	if err != nil {
		return err
	}

	for _, verdict := range snapshot.Verdicts {
		findings := make([]map[string]any, 0, len(verdict.Findings))
		for _, finding := range verdict.Findings {
			findings = append(findings, map[string]any{
				"hook": finding.Hook, "kind": finding.Kind,
				"path": finding.Path, "message": finding.Message,
				"feedback": finding.Feedback,
			})
		}
		tier := verdict.Tier
		if tier == "" {
			// The schema constrains tier to the two the protocol defines; a
			// verdict written without one is the supervisor's, since that is the
			// only tier whose verdicts reach a mailbox.
			tier = "supervisor"
		}
		if err := m.db.RecordGitAgentAttempt(ctx, database.RecordGitAgentAttemptInput{
			TaskID:          id,
			Attempt:         verdict.Attempt,
			Tier:            tier,
			Status:          database.GitAgentVerdictStatus(verdict.Status),
			ProtocolVersion: verdict.V,
			Findings:        findings,
			Feedback:        verdict.Feedback(),
		}); err != nil {
			return err
		}
	}

	if status, done := snapshot.Concluded(); done {
		return m.db.ConcludeGitAgentTask(ctx, id,
			gitAgentTerminalStatus(status), database.GitAgentVerdictStatus(status),
			snapshot.IntegratedBranch(), gitAgentConcludedAt(snapshot))
	}
	return nil
}

// gitAgentLiveStatus is the non-terminal state a scan can observe. The store
// never lets these overwrite a task that has already concluded.
func gitAgentLiveStatus(snapshot gitagent.TaskSnapshot) database.GitAgentTaskStatus {
	if snapshot.State != nil && snapshot.State.Attempts > 0 {
		return database.GitAgentTaskRunning
	}
	return database.GitAgentTaskDispatched
}

func gitAgentTerminalStatus(status gitagent.VerdictStatus) database.GitAgentTaskStatus {
	switch status {
	case gitagent.StatusAccepted:
		return database.GitAgentTaskAccepted
	case gitagent.StatusRejected:
		return database.GitAgentTaskRejected
	default:
		return database.GitAgentTaskErrored
	}
}

// gitAgentConcludedAt uses the task state's last write as the conclusion time.
// The verdict file carries no timestamp of its own, and state.json is rewritten
// as part of recording the verdict.
func gitAgentConcludedAt(snapshot gitagent.TaskSnapshot) time.Time {
	if snapshot.State != nil && !snapshot.State.UpdatedAt.IsZero() {
		return snapshot.State.UpdatedAt
	}
	return time.Now().UTC()
}
