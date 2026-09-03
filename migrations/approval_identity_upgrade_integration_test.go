package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	approvalIdentityMigration         = "74_turn_request_approval_identity.sql"
	providerApprovalIdentityMigration = "81_turn_request_provider_approval_identity.sql"
)

var _ = Describe("Tool approval identity migration", func() {
	It("backfills an unambiguous legacy approval and replaces the credential constraint", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_identity_upgrade"})
		dsn, db := handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())

		ids := seedLegacyToolApproval(ctx, db, 1)
		Expect(resetMigrationScripts(ctx, db, approvalIdentityMigration)).To(Succeed())
		Expect(Apply(ctx, dsn)).To(Succeed())

		var turnID, modelCallID uuid.UUID
		Expect(db.QueryRowContext(ctx, `
			SELECT turn_id, model_call_id
			FROM captain_turn_requests
			WHERE id = $1
		`, ids.request).Scan(&turnID, &modelCallID)).To(Succeed())
		Expect(turnID).To(Equal(ids.turn))
		Expect(modelCallID).To(Equal(ids.modelCalls[0]))

		// Identity is conditional on the credential: the caller-tool half keeps
		// the four-column shape, and the provider half is identified by its
		// prompt run and tool call alone.
		Expect(approvalIdentityConstraint(ctx, db)).To(And(
			ContainSubstring("credential_id IS NULL"),
			ContainSubstring("turn_id IS NOT NULL"),
			ContainSubstring("model_call_id IS NOT NULL"),
			Not(ContainSubstring("credential_id IS NOT NULL")),
		))

		Expect(insertProviderApproval(ctx, db, ids, "provider-call")).To(Succeed())
		Expect(Apply(ctx, dsn)).To(Succeed())
	})

	It("accepts a provider approval with no credential, turn or model call", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_identity_provider"})
		dsn, db := handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())

		ids := seedLegacyToolApproval(ctx, db, 1)
		Expect(resetMigrationScripts(ctx, db, approvalIdentityMigration)).To(Succeed())
		Expect(Apply(ctx, dsn)).To(Succeed())

		// This is the row `captain prompt run` writes: a session and a prompt run,
		// and nothing else. Rejecting it is what kept the durable approval broker
		// private to the aichat execution path.
		Expect(insertProviderApproval(ctx, db, ids, "credential-less-call")).To(Succeed())
	})

	It("still rejects a caller-tool approval that names no turn", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_identity_caller"})
		dsn, db := handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())

		ids := seedLegacyToolApproval(ctx, db, 1)
		Expect(resetMigrationScripts(ctx, db, approvalIdentityMigration)).To(Succeed())
		Expect(Apply(ctx, dsn)).To(Succeed())

		// A credential means the approval was raised inside a turn, so the turn
		// and model call are not optional: relaxing them for the provider path
		// must not relax them here.
		_, err := db.ExecContext(ctx, `
			INSERT INTO captain_turn_requests (
				id, session_id, prompt_run_id, credential_id, tool_call_id,
				kind, request, idempotency_key, requested_by, expires_at
			) VALUES ($1, $2, $3, $4, 'turnless-caller-call', 'tool_approval',
				'{"tool":"accounts_edit","input":{}}', $5, 'caller_tool', $6)
		`, uuid.New(), ids.session, ids.promptRun, ids.credential,
			"mcp:"+ids.credential.String()+":turnless-caller-call", time.Now().Add(time.Hour))
		Expect(err).To(MatchError(ContainSubstring("captain_turn_requests_tool_approval_identity")))
	})

	It("re-applies the retired 74 and its successor with a provider approval already stored", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_identity_rerun"})
		dsn, db := handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())

		ids := seedLegacyToolApproval(ctx, db, 1)
		Expect(resetMigrationScripts(ctx, db, approvalIdentityMigration)).To(Succeed())
		Expect(Apply(ctx, dsn)).To(Succeed())
		Expect(insertProviderApproval(ctx, db, ids, "provider-call")).To(Succeed())

		// A content-hash change, a ledger reset, a restore: any of them re-runs
		// both scripts. 74's original guard read this NULL-turn provider row as
		// an ambiguous legacy row and raised, so one stored provider approval
		// blocked startup forever after.
		Expect(resetMigrationScripts(ctx, db,
			approvalIdentityMigration, providerApprovalIdentityMigration)).To(Succeed())
		Expect(Apply(ctx, dsn)).To(Succeed())

		Expect(approvalIdentityConstraint(ctx, db)).To(ContainSubstring("credential_id IS NULL"),
			"81 runs after 74 and reinstates the final shape")
	})

	It("fails when a legacy approval cannot be correlated to one model call", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_identity_ambiguous"})
		dsn, db := handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())

		ids := seedLegacyToolApproval(ctx, db, 2)
		Expect(resetMigrationScripts(ctx, db, approvalIdentityMigration)).To(Succeed())
		err := Apply(ctx, dsn)
		Expect(err).To(MatchError(And(
			ContainSubstring("ambiguous legacy tool approval identity"),
			ContainSubstring(ids.request.String()),
		)))
	})

	It("rejects a legacy constraint after its upgrade script is already recorded", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_identity_drift"})
		dsn, db := handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())
		Expect(installLegacyApprovalConstraint(ctx, db)).To(Succeed())

		err := Apply(ctx, dsn)
		Expect(err).To(MatchError(And(
			ContainSubstring("verify Captain database"),
			ContainSubstring("credential_id IS NOT NULL"),
		)))
	})
})

type legacyApprovalIDs struct {
	session    uuid.UUID
	turn       uuid.UUID
	promptRun  uuid.UUID
	request    uuid.UUID
	credential uuid.UUID
	modelCalls []uuid.UUID
}

// insertProviderApproval writes the row a credential-less provider run raises:
// a session and a prompt run, with no credential, turn or model call.
func insertProviderApproval(ctx context.Context, db *sql.DB, ids legacyApprovalIDs, toolCallID string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO captain_turn_requests (
			id, session_id, prompt_run_id, tool_call_id,
			kind, request, idempotency_key, requested_by, expires_at
		) VALUES ($1, $2, $3, $4, 'tool_approval',
			'{"tool":"accounts_edit","input":{}}', $5, 'provider', $6)
	`, uuid.New(), ids.session, ids.promptRun, toolCallID,
		"provider:"+ids.promptRun.String()+":"+toolCallID, time.Now().Add(time.Hour))
	return err
}

func approvalIdentityConstraint(ctx context.Context, db *sql.DB) string {
	GinkgoHelper()
	var definition string
	Expect(db.QueryRowContext(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conname = 'captain_turn_requests_tool_approval_identity'
	`).Scan(&definition)).To(Succeed())
	return definition
}

func seedLegacyToolApproval(ctx context.Context, db *sql.DB, modelCallCount int) legacyApprovalIDs {
	ids := legacyApprovalIDs{
		session: uuid.New(), turn: uuid.New(), promptRun: uuid.New(), request: uuid.New(),
	}
	Expect(installLegacyApprovalConstraint(ctx, db)).To(Succeed())

	_, err := db.ExecContext(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'aichat')`, ids.session)
	Expect(err).NotTo(HaveOccurred())
	_, err = db.ExecContext(ctx, `
		INSERT INTO captain_turns (id, session_id, provider_turn_id, turn_index, status, started_at)
		VALUES ($1, $2, 'legacy-turn', 0, 'open', now())
	`, ids.turn, ids.session)
	Expect(err).NotTo(HaveOccurred())
	_, err = db.ExecContext(ctx, `
		INSERT INTO captain_prompt_runs (id, session_id, turn_id, root_session_id, admission_key)
		VALUES ($1, $2, $3, $2, 'legacy-approval-run')
	`, ids.promptRun, ids.session, ids.turn)
	Expect(err).NotTo(HaveOccurred())

	for i := range modelCallCount {
		modelCallID := uuid.New()
		ids.modelCalls = append(ids.modelCalls, modelCallID)
		_, err = db.ExecContext(ctx, `
			INSERT INTO captain_model_calls (
				id, turn_id, prompt_run_id, call_index, model, provider, mode, status, started_at
			) VALUES ($1, $2, $3, $4, 'gemini', 'google', 'api', 'running', now())
		`, modelCallID, ids.turn, ids.promptRun, i)
		Expect(err).NotTo(HaveOccurred())
	}

	credentialID := uuid.New()
	ids.credential = credentialID
	_, err = db.ExecContext(ctx, `
		INSERT INTO captain_session_mcp_credentials (
			id, session_id, prompt_run_id, provider, mode, secret_hash, policy, expires_at
		) VALUES ($1, $2, $3, 'google', 'api', $4, '{"accounts_edit":"ask"}', $5)
	`, credentialID, ids.session, ids.promptRun, []byte(strings.Repeat("a", 32)), time.Now().Add(time.Hour))
	Expect(err).NotTo(HaveOccurred())
	_, err = db.ExecContext(ctx, `
		INSERT INTO captain_turn_requests (
			id, session_id, prompt_run_id, credential_id, tool_call_id, kind,
			request, idempotency_key, requested_by, expires_at
		) VALUES ($1, $2, $3, $4, 'legacy-call', 'tool_approval',
			'{"tool":"accounts_edit","input":{}}', $5, 'caller_tool', $6)
	`, ids.request, ids.session, ids.promptRun, credentialID,
		"mcp:"+credentialID.String()+":legacy-call", time.Now().Add(time.Hour))
	Expect(err).NotTo(HaveOccurred())
	return ids
}

func installLegacyApprovalConstraint(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		ALTER TABLE captain_turn_requests
		DROP CONSTRAINT captain_turn_requests_tool_approval_identity
	`); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
		ALTER TABLE captain_turn_requests
		ADD CONSTRAINT captain_turn_requests_tool_approval_identity
		CHECK (kind <> 'tool_approval' OR (
			credential_id IS NOT NULL AND prompt_run_id IS NOT NULL AND tool_call_id IS NOT NULL
		))
	`)
	return err
}

// resetMigrationScripts forgets the named scripts' recorded hashes so the next
// Apply re-runs them — the ledger state a restore, a rebuilt environment or a
// content-hash change puts a database in.
func resetMigrationScripts(ctx context.Context, db *sql.DB, paths ...string) error {
	for _, path := range paths {
		result, err := db.ExecContext(ctx, `
			DELETE FROM schema_migration_scripts
			WHERE scope = $1 AND path = $2
		`, Scope, path)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read reset migration result for %s: %w", path, err)
		}
		if affected != 1 {
			return fmt.Errorf("reset migration ledger for %s: deleted %d rows, want 1", path, affected)
		}
	}
	return nil
}
