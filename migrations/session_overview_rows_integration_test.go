package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const sessionOverviewChannel = "captain_session_overview_change"

// querier is a pool or an open transaction: inside a transaction the
// overview is compared with what that transaction sees.
type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// overviewDrift returns every row the maintained session and prompt-run
// overview tables and their authoritative recomputes disagree on, in both
// directions, as JSON.
func overviewDrift(ctx context.Context, db querier) []string {
	GinkgoHelper()
	var columns string
	Expect(db.QueryRowContext(ctx, `
		SELECT string_agg(quote_ident(column_name), ', ' ORDER BY ordinal_position)
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'captain_session_overview_compute'`).Scan(&columns)).To(Succeed())
	sessionRows := "SELECT " + columns + " FROM captain_session_overview_rows"
	sessionCompute := "SELECT " + columns + " FROM captain_session_overview_compute"
	runRows := "SELECT * FROM captain_prompt_run_overview_rows"
	runRebuild := "SELECT * FROM captain_prompt_run_overview_rebuild()"
	var drift []string
	for _, direction := range [][3]string{
		{"session rows", sessionRows, sessionCompute},
		{"session compute", sessionCompute, sessionRows},
		{"run rows", runRows, runRebuild},
		{"run rebuild", runRebuild, runRows},
	} {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(
			`SELECT 'only in %s: ' || to_jsonb(d)::text FROM (%s EXCEPT %s) d`, direction[0], direction[1], direction[2]))
		Expect(err).NotTo(HaveOccurred())
		for rows.Next() {
			var row string
			Expect(rows.Scan(&row)).To(Succeed())
			drift = append(drift, row)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(rows.Close()).To(Succeed())
	}
	return drift
}

func drainOverviewNotifications(ctx context.Context, listener *pgx.Conn) []string {
	GinkgoHelper()
	var payloads []string
	for {
		waitCtx, cancel := context.WithTimeout(ctx, quietWindow)
		notification, err := listener.WaitForNotification(waitCtx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			return payloads
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(notification.Channel).To(Equal(sessionOverviewChannel))
		payloads = append(payloads, notification.Payload)
	}
}

var _ = Describe("session overview rows", Ordered, func() {
	var (
		dsn string
		db  *sql.DB
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_overview_rows"})
		dsn, db = handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())
	})

	exec := func(ctx context.Context, query string, args ...any) {
		GinkgoHelper()
		_, err := db.ExecContext(ctx, query, args...)
		Expect(err).NotTo(HaveOccurred(), query)
	}

	It("matches the rebuild query after every kind of source write", func(ctx SpecContext) {
		root, agent, other := uuid.New(), uuid.New(), uuid.New()
		turn, call, run, plan := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		steps := []struct {
			name  string
			query string
			args  []any
		}{
			{"sessions", `INSERT INTO captain_sessions (id, source, title, cwd, started_at) VALUES
				($1, 'claude', 'root', '/work/a', now() - interval '1 hour'), ($2, 'codex', 'other', '/work/b', now())`,
				[]any{root, other}},
			{"agent child", `INSERT INTO captain_sessions (id, source, parent_session_id, root_session_id, parent_relation)
				VALUES ($1, 'claude', $2, $2, 'agent')`, []any{agent, root}},
			{"session update", `UPDATE captain_sessions SET title = 'renamed', path = '/work/a/t.jsonl', last_activity_at = now() WHERE id = $1`, []any{root}},
			{"process start", `INSERT INTO captain_session_processes (session_id, host_id, boot_id, pid, process_started_at, status, cwd)
				VALUES ($1, 'host', 'boot', 4242, now(), 'active', '/work/a')`, []any{root}},
			{"process heartbeat", `UPDATE captain_session_processes SET last_heartbeat_at = now(), cpu_percent = 12.5 WHERE session_id = $1`, []any{root}},
			{"source register", `INSERT INTO captain_session_sources (session_id, source_kind, path) VALUES ($1, 'claude', '/work/a/one.jsonl')`, []any{root}},
			{"source replace", `INSERT INTO captain_session_sources (session_id, source_kind, path, updated_at)
				VALUES ($1, 'claude-alt', '/work/a/two.jsonl', now() + interval '1 minute')`, []any{root}},
			{"turn", `INSERT INTO captain_turns (id, session_id, turn_index) VALUES ($1, $2, 0)`, []any{turn, root}},
			{"model call", `INSERT INTO captain_model_calls (id, turn_id, call_index, model, provider, input_tokens, context_tokens, context_window_tokens, context_free_percent, started_at)
				VALUES ($1, $2, 0, 'model-a', 'acme', 10, 20, 100, 80, now())`, []any{call, turn}},
			{"model call stream", `UPDATE captain_model_calls SET output_tokens = output_tokens + 7, input_cost = input_cost + 0.125 WHERE id = $1`, []any{call}},
			{"model call stream again", `UPDATE captain_model_calls SET output_tokens = output_tokens + 3, provider_cost_usd = 0.5, ended_at = now() WHERE id = $1`, []any{call}},
			{"model call upsert batch", `INSERT INTO captain_model_calls (turn_id, call_index, model, input_tokens, cache_read_tokens, input_cost, started_at)
				VALUES ($1, 1, 'model-b', 5, 50, 0.01, now() + interval '1 second'), ($1, 0, 'model-a', 99, 0, 0, now())
				ON CONFLICT (turn_id, call_index) DO UPDATE SET input_tokens = EXCLUDED.input_tokens, cache_read_tokens = EXCLUDED.cache_read_tokens`, []any{turn}},
			{"non-USD call", `INSERT INTO captain_model_calls (turn_id, call_index, model, input_tokens, input_cost, currency, created_at)
				VALUES ($1, 2, 'model-c', 1, 9, 'EUR', now() - interval '1 day')`, []any{turn}},
			{"message batch", `INSERT INTO captain_messages (session_id, sequence, role, parts)
				SELECT $1, n, 'assistant', CASE WHEN n % 3 = 0
					THEN '[{"type":"tool-shell","toolName":"shell"},{"type":"dynamic-tool","toolName":"mcp"},{"type":"text","text":"x"}]'::jsonb
					ELSE '[{"type":"text","text":"y"}]'::jsonb END
				FROM generate_series(0, 49) AS n`, []any{root}},
			{"message enriched", `UPDATE captain_messages SET parts = '[{"type":"tool-read","toolName":""}]'::jsonb WHERE session_id = $1 AND sequence = 3`, []any{root}},
			{"message deleted", `DELETE FROM captain_messages WHERE session_id = $1 AND sequence IN (0, 1)`, []any{root}},
			{"events", `INSERT INTO captain_events (session_id, event_key, stream, sequence, kind)
				SELECT $1, 'e' || n, 'main', n, 'note' FROM generate_series(0, 4) AS n`, []any{root}},
			{"event deleted", `DELETE FROM captain_events WHERE session_id = $1 AND sequence = 4`, []any{root}},
			{"prompt run", `INSERT INTO captain_prompt_runs (id, session_id, root_session_id, runtime) VALUES ($2, $1, $1, '{"mode":"cli"}'::jsonb)`, []any{root, run}},
			{"prompt run executes elsewhere", `UPDATE captain_prompt_runs SET execution_session_id = $2, runtime = '{"mode":"sdk"}'::jsonb WHERE session_id = $1`, []any{root, other}},
			{"iterations", `INSERT INTO captain_prompt_run_iterations (prompt_run_id, iteration) VALUES ($1, 0), ($1, 1)`, []any{run}},
			{"iteration settled", `UPDATE captain_prompt_run_iterations SET state = 'succeeded' WHERE prompt_run_id = $1 AND iteration = 0`, []any{run}},
			{"run model call", `INSERT INTO captain_model_calls (turn_id, call_index, model, prompt_run_id, input_tokens, output_cost, started_at)
				VALUES ($1, 3, 'model-r', $2, 11, 0.2, now() - interval '1 hour')`, []any{turn, run}},
			{"plan", `INSERT INTO captain_plans (id, source_session_id, source_prompt_run_id, title) VALUES ($3, $1, $2, 'plan')`, []any{root, run, plan}},
			{"plan revision", `INSERT INTO captain_plan_revisions (plan_id, revision, plan_markdown, content_hash) VALUES ($1, 1, '# plan', 'hash-1')`, []any{plan}},
			{"turn request", `INSERT INTO captain_turn_requests (session_id, turn_id, prompt_run_id, kind) VALUES ($1, $2, $3, 'question')`, []any{root, turn, run}},
			{"turn request answered", `UPDATE captain_turn_requests SET state = 'answered' WHERE session_id = $1`, []any{root}},
			{"artifacts", `INSERT INTO captain_artifacts (session_id, kind, path) VALUES ($1, 'file.read', '/a'), ($1, 'file.write', '/b'), ($1, 'note', '/c')`, []any{root}},
			{"process end", `UPDATE captain_session_processes SET ended_at = now() WHERE session_id = $1`, []any{root}},
			{"reparent", `UPDATE captain_sessions SET parent_session_id = $2, root_session_id = $2 WHERE id = $1`, []any{agent, other}},
			{"latest call deleted", `DELETE FROM captain_model_calls WHERE turn_id = $1 AND call_index = 1`, []any{turn}},
			{"turn deleted", `DELETE FROM captain_turns WHERE id = $1`, []any{turn}},
			{"session deleted", `DELETE FROM captain_sessions WHERE id = $1`, []any{other}},
		}
		for _, step := range steps {
			_, err := db.ExecContext(ctx, step.query, step.args...)
			Expect(err).NotTo(HaveOccurred(), step.name)
			Expect(overviewDrift(ctx, db)).To(BeEmpty(), step.name)
		}

		var remaining int
		Expect(db.QueryRowContext(ctx, `SELECT count(*) FROM captain_session_overview_rows`).Scan(&remaining)).To(Succeed())
		Expect(remaining).To(Equal(1), "deleting a session cascades to its reparented agent child and both overview rows")
	})

	It("refills a truncated table through ensure and heals rows written before it", func(ctx SpecContext) {
		ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
		for _, id := range ids {
			exec(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'claude')`, id)
			exec(ctx, `INSERT INTO captain_messages (session_id, sequence, role, parts) VALUES ($1, 0, 'user', '[]'::jsonb)`, id)
		}
		turn, call := uuid.New(), uuid.New()
		exec(ctx, `INSERT INTO captain_turns (id, session_id, turn_index) VALUES ($1, $2, 0)`, turn, ids[1])
		exec(ctx, `INSERT INTO captain_model_calls (id, turn_id, call_index, model, input_tokens) VALUES ($1, $2, 0, 'model-a', 4)`, call, turn)
		exec(ctx, `TRUNCATE captain_session_overview_rows`)

		// A commit touching a session whose row is missing never raises: the
		// flush recomputes that session from source, as a long-lived writer
		// that outlived a PostgreSQL restart needs. The session the previous
		// spec left and ids[2] are still missing.
		exec(ctx, `INSERT INTO captain_messages (session_id, sequence, role, parts) VALUES ($1, 1, 'user', '[]'::jsonb)`, ids[0])
		exec(ctx, `UPDATE captain_model_calls SET output_tokens = 9 WHERE id = $1`, call)
		Expect(overviewDrift(ctx, db)).To(HaveLen(2))

		var rebuilt bool
		Expect(db.QueryRowContext(ctx, `SELECT captain_session_overview_ensure(1)`).Scan(&rebuilt)).To(Succeed())
		Expect(rebuilt).To(BeTrue())
		Expect(db.QueryRowContext(ctx, `SELECT captain_session_overview_ensure()`).Scan(&rebuilt)).To(Succeed())
		Expect(rebuilt).To(BeTrue())
		Expect(db.QueryRowContext(ctx, `SELECT captain_session_overview_ensure()`).Scan(&rebuilt)).To(Succeed())
		Expect(rebuilt).To(BeFalse())
		Expect(overviewDrift(ctx, db)).To(BeEmpty())
	})

	It("notifies a rendered change and stays silent for a no-op or heartbeat", func(ctx SpecContext) {
		listener, err := pgx.Connect(ctx, dsn)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(listener.Close(context.Background())).To(Succeed()) })
		_, err = listener.Exec(ctx, "LISTEN "+sessionOverviewChannel)
		Expect(err).NotTo(HaveOccurred())

		id := uuid.New()
		exec(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'claude')`, id)
		exec(ctx, `INSERT INTO captain_session_processes (session_id, host_id, boot_id, pid, process_started_at, status)
			VALUES ($1, 'host', 'boot', 777, now(), 'active')`, id)
		Expect(drainOverviewNotifications(ctx, listener)).To(Equal([]string{id.String(), id.String()}))

		exec(ctx, `UPDATE captain_sessions SET title = title WHERE id = $1`, id)
		exec(ctx, `UPDATE captain_session_processes SET last_heartbeat_at = now(), cpu_percent = 3 WHERE session_id = $1`, id)
		Expect(drainOverviewNotifications(ctx, listener)).To(BeEmpty())

		exec(ctx, `INSERT INTO captain_messages (session_id, sequence, role, parts) VALUES ($1, 0, 'user', '[]'::jsonb)`, id)
		Expect(drainOverviewNotifications(ctx, listener)).To(Equal([]string{id.String()}))
	})

	It("keeps the unlogged table and its rows when the bundle is applied again", func(ctx SpecContext) {
		var before int
		Expect(db.QueryRowContext(ctx, `SELECT count(*) FROM captain_session_overview_rows`).Scan(&before)).To(Succeed())
		Expect(before).To(BeNumerically(">", 0))

		Expect(Apply(ctx, dsn)).To(Succeed())

		var after int
		var persistence string
		Expect(db.QueryRowContext(ctx, `SELECT count(*) FROM captain_session_overview_rows`).Scan(&after)).To(Succeed())
		Expect(db.QueryRowContext(ctx,
			`SELECT relpersistence FROM pg_class WHERE oid = 'public.captain_session_overview_rows'::regclass`).Scan(&persistence)).To(Succeed())
		Expect(after).To(Equal(before))
		Expect(persistence).To(Equal("u"))
		Expect(overviewDrift(ctx, db)).To(BeEmpty())
	})

	insertSession := func(ctx context.Context) uuid.UUID {
		GinkgoHelper()
		id := uuid.New()
		exec(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'claude')`, id)
		return id
	}
	begin := func(ctx context.Context) *sql.Tx {
		GinkgoHelper()
		tx, err := db.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = tx.Rollback() })
		return tx
	}
	txExec := func(ctx context.Context, tx *sql.Tx, query string, args ...any) {
		GinkgoHelper()
		_, err := tx.ExecContext(ctx, query, args...)
		Expect(err).NotTo(HaveOccurred(), query)
	}
	queued := func(ctx context.Context, tx *sql.Tx) []string {
		GinkgoHelper()
		rows, err := tx.QueryContext(ctx, `SELECT kind || ':' || id FROM captain_overview_pending
			WHERE txid = pg_current_xact_id() ORDER BY kind, id`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var entries []string
		for rows.Next() {
			var entry string
			Expect(rows.Scan(&entry)).To(Succeed())
			entries = append(entries, entry)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return entries
	}

	It("queues a message burst once, is stale inside the transaction and exact after commit", func(ctx SpecContext) {
		session := insertSession(ctx)
		tx := begin(ctx)
		for sequence := 0; sequence < 50; sequence++ {
			txExec(ctx, tx, `INSERT INTO captain_messages (session_id, sequence, role, parts, occurred_at)
				VALUES ($1, $2, 'assistant', '[{"type":"tool-shell","toolName":"shell"}]'::jsonb, now())`, session, sequence)
		}
		txExec(ctx, tx, `INSERT INTO captain_messages (session_id, sequence, role, parts)
			SELECT $1, n, 'assistant', '[]'::jsonb FROM generate_series(50, 99) AS n`, session)

		Expect(queued(ctx, tx)).To(Equal([]string{"session:" + session.String()}),
			"a hundred message writes queue one recompute for their session")
		Expect(overviewDrift(ctx, tx)).NotTo(BeEmpty(), "the open transaction has not flushed")
		Expect(tx.Commit()).To(Succeed())
		Expect(overviewDrift(ctx, db)).To(BeEmpty())
	})

	It("flushes after every statement under SET CONSTRAINTS ALL IMMEDIATE", func(ctx SpecContext) {
		session := insertSession(ctx)
		tx := begin(ctx)
		txExec(ctx, tx, `SET CONSTRAINTS ALL IMMEDIATE`)
		for sequence := 0; sequence < 2; sequence++ {
			txExec(ctx, tx, `INSERT INTO captain_messages (session_id, sequence, role, parts) VALUES ($1, $2, 'user', '[]'::jsonb)`,
				session, sequence)
			Expect(queued(ctx, tx)).To(BeEmpty(), "the statement's flush took its queue")
			Expect(overviewDrift(ctx, tx)).To(BeEmpty(), "write %d re-queued and re-flushed", sequence)
		}
		Expect(tx.Commit()).To(Succeed())
		Expect(overviewDrift(ctx, db)).To(BeEmpty())
	})

	It("skips a session the committing transaction created and deleted", func(ctx SpecContext) {
		id := uuid.New()
		tx := begin(ctx)
		txExec(ctx, tx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'claude')`, id)
		txExec(ctx, tx, `INSERT INTO captain_messages (session_id, sequence, role, parts) VALUES ($1, 0, 'user', '[]'::jsonb)`, id)
		txExec(ctx, tx, `DELETE FROM captain_sessions WHERE id = $1`, id)
		Expect(tx.Commit()).To(Succeed())

		var rows int
		Expect(db.QueryRowContext(ctx, `SELECT count(*) FROM captain_session_overview_rows WHERE id = $1`, id).Scan(&rows)).To(Succeed())
		Expect(rows).To(BeZero())
		Expect(overviewDrift(ctx, db)).To(BeEmpty())
	})

	// Mirrors pkg/aichat's approval interleaving: an open transaction that
	// wrote a run's request and usage must not hold anything a concurrent run
	// and session update waits on.
	It("lets a concurrent run and session update commit past an open run writer", func(ctx SpecContext) {
		session, run, turn := insertSession(ctx), uuid.New(), uuid.New()
		exec(ctx, `INSERT INTO captain_prompt_runs (id, session_id, root_session_id) VALUES ($1, $2, $2)`, run, session)
		exec(ctx, `INSERT INTO captain_turns (id, session_id, turn_index) VALUES ($1, $2, 0)`, turn, session)

		writer := begin(ctx)
		txExec(ctx, writer, `INSERT INTO captain_turn_requests (session_id, turn_id, prompt_run_id, kind) VALUES ($1, $2, $3, 'question')`,
			session, turn, run)
		txExec(ctx, writer, `INSERT INTO captain_model_calls (turn_id, call_index, model, prompt_run_id, input_tokens)
			VALUES ($1, 0, 'model-a', $2, 7)`, turn, run)

		updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		updater, err := db.BeginTx(updateCtx, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = updater.ExecContext(updateCtx, `UPDATE captain_prompt_runs SET current_iteration = 3 WHERE id = $1`, run)
		Expect(err).NotTo(HaveOccurred(), "the run update must not wait on the open writer")
		_, err = updater.ExecContext(updateCtx, `UPDATE captain_sessions SET title = 'concurrent' WHERE id = $1`, session)
		Expect(err).NotTo(HaveOccurred(), "the session update must not wait on the open writer")
		Expect(updater.Commit()).To(Succeed(), "the updater's flush must not wait on the open writer")

		Expect(writer.Commit()).To(Succeed())
		Expect(overviewDrift(ctx, db)).To(BeEmpty())
		var pending, current int64
		Expect(db.QueryRowContext(ctx, `SELECT pending_turn_request_count, current_iteration
			FROM captain_prompt_run_overview_rows WHERE id = $1`, run).Scan(&pending, &current)).To(Succeed())
		Expect([]int64{pending, current}).To(Equal([]int64{1, 3}), "each transaction's change survived the other's flush")
	})
})
