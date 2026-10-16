package database

import (
	"context"
	"database/sql"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The host functions stand in for gavel's todo_issue_overview projection: a
// recorder that writes every call into a host table, and a hook that refuses
// every change.
const (
	hookRecorder  = "public.host_record_row" + RowHookSignature
	hookRejecter  = "public.host_reject_row" + RowHookSignature
	hookOwnerName = "acme-host"
)

// hookCall is one recorded hook invocation; an invalid id is an absent identity.
type hookCall struct {
	Table, Op                string
	ID                       uuid.UUID
	Session, Root, PromptRun uuid.NullUUID
}

func present(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: true} }

var _ = Describe("Captain row hooks", Ordered, func() {
	var (
		db  *DB
		raw *sql.DB
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_row_hook"})
		var err error
		db, err = Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		raw, err = db.Gorm().DB()
		Expect(err).NotTo(HaveOccurred())
		Expect(Migrate(ctx, handle.DSN())).To(Succeed(), "a second apply must be idempotent")

		for _, statement := range []string{
			`CREATE TABLE public.host_row_calls (seq bigserial PRIMARY KEY, tbl text, op text,
				id uuid, session_id uuid, root_session_id uuid, prompt_run_id uuid)`,
			`CREATE FUNCTION public.host_record_row(p_table text, p_op text, p_id uuid,
				p_session_id uuid, p_root_session_id uuid, p_prompt_run_id uuid) RETURNS void LANGUAGE sql AS $$
				INSERT INTO public.host_row_calls (tbl, op, id, session_id, root_session_id, prompt_run_id)
				VALUES (p_table, p_op, p_id, p_session_id, p_root_session_id, p_prompt_run_id) $$`,
			`CREATE FUNCTION public.host_reject_row(p_table text, p_op text, p_id uuid,
				p_session_id uuid, p_root_session_id uuid, p_prompt_run_id uuid) RETURNS void LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'host projection refused % %', p_op, p_id; END $$`,
			`CREATE FUNCTION public.host_narrow_row(p_table text, p_op text) RETURNS void LANGUAGE sql AS $$ SELECT $$`,
			`CREATE FUNCTION public.host_counting_row(p_table text, p_op text, p_id uuid,
				p_session_id uuid, p_root_session_id uuid, p_prompt_run_id uuid) RETURNS integer LANGUAGE sql AS $$ SELECT 1 $$`,
		} {
			_, err = raw.ExecContext(ctx, statement)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	exec := func(ctx context.Context, query string, args ...any) error {
		_, err := raw.ExecContext(ctx, query, args...)
		return err
	}
	recorder := func(table RowHookTable) RowHook {
		return RowHook{Table: table, Function: hookRecorder, Owner: hookOwnerName}
	}
	registerRecorder := func(ctx context.Context, tables ...RowHookTable) {
		GinkgoHelper()
		for _, table := range tables {
			Expect(db.RegisterRowHook(ctx, recorder(table))).To(Succeed())
			DeferCleanup(func(ctx SpecContext) { Expect(db.UnregisterRowHook(ctx, recorder(table))).To(Succeed()) })
		}
	}
	// takeCalls returns and clears the recorded calls in call order.
	takeCalls := func(ctx context.Context) []hookCall {
		GinkgoHelper()
		rows, err := raw.QueryContext(ctx, `WITH taken AS (DELETE FROM public.host_row_calls RETURNING *)
			SELECT tbl, op, id, session_id, root_session_id, prompt_run_id FROM taken ORDER BY seq`)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = rows.Close() }()
		var calls []hookCall
		for rows.Next() {
			var call hookCall
			Expect(rows.Scan(&call.Table, &call.Op, &call.ID, &call.Session, &call.Root, &call.PromptRun)).To(Succeed())
			calls = append(calls, call)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return calls
	}
	newSession := func(ctx context.Context) uuid.UUID {
		GinkgoHelper()
		id := uuid.New()
		Expect(exec(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'gavel')`, id)).To(Succeed())
		return id
	}
	newRun := func(ctx context.Context, session uuid.UUID) uuid.UUID {
		GinkgoHelper()
		run := uuid.New()
		Expect(exec(ctx, `INSERT INTO captain_prompt_runs (id, session_id, root_session_id) VALUES ($1, $2, $2)`,
			run, session)).To(Succeed())
		return run
	}
	sessionCall := func(op string, id uuid.UUID) hookCall {
		return hookCall{Table: "captain_sessions", Op: op, ID: id, Session: present(id), Root: present(id)}
	}
	runCall := func(op string, run, session uuid.UUID) hookCall {
		return hookCall{Table: "captain_prompt_runs", Op: op, ID: run,
			Session: present(session), Root: present(session), PromptRun: present(run)}
	}
	requestCall := func(op string, request, session, run uuid.UUID) hookCall {
		return hookCall{Table: "captain_turn_requests", Op: op, ID: request, Session: present(session), PromptRun: present(run)}
	}

	It("calls an overview hook after the overview row settled, and not for an unrendered change", func(ctx SpecContext) {
		Expect(exec(ctx, `CREATE TABLE IF NOT EXISTS public.host_overview_reads (seq bigserial PRIMARY KEY, id uuid, iterations bigint)`)).To(Succeed())
		Expect(exec(ctx, `CREATE OR REPLACE FUNCTION public.host_read_run_overview(p_table text, p_op text, p_id uuid,
			p_session_id uuid, p_root_session_id uuid, p_prompt_run_id uuid) RETURNS void LANGUAGE sql AS $$
			INSERT INTO public.host_overview_reads (id, iterations)
			SELECT id, iteration_count FROM public.captain_prompt_run_overview_rows WHERE id = p_id $$`)).To(Succeed())
		reader := RowHook{Table: RowHookPromptRunOverview, Function: "public.host_read_run_overview" + RowHookSignature, Owner: hookOwnerName}
		Expect(db.RegisterRowHook(ctx, reader)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(db.UnregisterRowHook(ctx, reader)).To(Succeed()) })
		registerRecorder(ctx, RowHookSessionOverview)
		readIterations := func() []int64 {
			GinkgoHelper()
			rows, err := raw.QueryContext(ctx, `WITH taken AS (DELETE FROM public.host_overview_reads RETURNING *)
				SELECT iterations FROM taken ORDER BY seq`)
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = rows.Close() }()
			var seen []int64
			for rows.Next() {
				var n int64
				Expect(rows.Scan(&n)).To(Succeed())
				seen = append(seen, n)
			}
			return seen
		}

		session := newSession(ctx)
		Expect(takeCalls(ctx)).To(ConsistOf(hookCall{Table: "captain_session_overview_rows", Op: "UPDATE",
			ID: session, Session: present(session), Root: present(session)}),
			"one call, for the filled row, not the rebuild's id-only placeholder")
		run := newRun(ctx, session)
		takeCalls(ctx)
		Expect(readIterations()).To(Equal([]int64{0}), "the run's overview row exists when its hook runs")

		Expect(exec(ctx, `INSERT INTO captain_prompt_run_iterations (prompt_run_id, iteration) VALUES ($1, 0)`, run)).To(Succeed())
		Expect(readIterations()).To(Equal([]int64{1}), "the hook reads the iteration the statement-level trigger counted")

		Expect(exec(ctx, `UPDATE captain_prompt_run_iterations SET request = request WHERE prompt_run_id = $1`, run)).To(Succeed())
		Expect(exec(ctx, `UPDATE captain_sessions SET updated_at = now() WHERE id = $1`, session)).To(Succeed())
		Expect(readIterations()).To(BeEmpty(), "a no-op iteration update leaves the overview row, so no hook")
		Expect(takeCalls(ctx)).To(BeEmpty(), "updated_at is not rendered, so the session overview hook stays silent")
		Expect(exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, session)).To(Succeed())
	})

	It("lets every write succeed while no hook is registered", func(ctx SpecContext) {
		session := newSession(ctx)
		run := newRun(ctx, session)
		Expect(exec(ctx, `UPDATE captain_prompt_runs SET current_iteration = 2 WHERE id = $1`, run)).To(Succeed())
		Expect(exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, session)).To(Succeed())
		Expect(takeCalls(ctx)).To(BeEmpty())
	})

	It("calls the hook with each session insert, update and delete identity", func(ctx SpecContext) {
		registerRecorder(ctx, RowHookSessions)
		session := newSession(ctx)
		Expect(exec(ctx, `UPDATE captain_sessions SET title = 'renamed' WHERE id = $1`, session)).To(Succeed())
		Expect(exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, session)).To(Succeed())
		Expect(takeCalls(ctx)).To(Equal([]hookCall{
			sessionCall("INSERT", session), sessionCall("UPDATE", session), sessionCall("DELETE", session),
		}))
	})

	It("calls the hook with the old and the new identity when an update re-points a row", func(ctx SpecContext) {
		session := newSession(ctx)
		first := newRun(ctx, session)
		Expect(exec(ctx, `UPDATE captain_prompt_runs SET state = 'succeeded', phase = 'finished' WHERE id = $1`, first)).To(Succeed())
		second, request := newRun(ctx, session), uuid.New()
		Expect(exec(ctx, `INSERT INTO captain_turn_requests (id, session_id, prompt_run_id, kind) VALUES ($1, $2, $3, 'question')`,
			request, session, first)).To(Succeed())
		registerRecorder(ctx, RowHookTurnRequests)

		Expect(exec(ctx, `UPDATE captain_turn_requests SET prompt_run_id = $2 WHERE id = $1`, request, second)).To(Succeed())
		Expect(exec(ctx, `UPDATE captain_turn_requests SET reason = 'clarified' WHERE id = $1`, request)).To(Succeed())
		Expect(takeCalls(ctx)).To(Equal([]hookCall{
			requestCall("UPDATE", request, session, first),
			requestCall("UPDATE", request, session, second),
			requestCall("UPDATE", request, session, second),
		}), "an update that keeps the identity is called once")
	})

	It("calls the hook for every row a session delete cascades to", func(ctx SpecContext) {
		session := newSession(ctx)
		run := newRun(ctx, session)
		iteration, request := uuid.New(), uuid.New()
		Expect(exec(ctx, `INSERT INTO captain_prompt_run_iterations (id, prompt_run_id, iteration) VALUES ($1, $2, 1)`,
			iteration, run)).To(Succeed())
		Expect(exec(ctx, `INSERT INTO captain_turn_requests (id, session_id, prompt_run_id, kind) VALUES ($1, $2, $3, 'question')`,
			request, session, run)).To(Succeed())
		registerRecorder(ctx, RowHookSessions, RowHookPromptRuns, RowHookTurnRequests, RowHookPromptRunIterations)

		Expect(exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, session)).To(Succeed())
		Expect(takeCalls(ctx)).To(ConsistOf(
			sessionCall("DELETE", session),
			runCall("DELETE", run, session),
			hookCall{Table: "captain_prompt_run_iterations", Op: "DELETE", ID: iteration, PromptRun: present(run)},
			requestCall("DELETE", request, session, run),
		))
	})

	It("still calls the hook while captain.suppress_session_change silences followers", func(ctx SpecContext) {
		registerRecorder(ctx, RowHookPromptRuns)
		session := newSession(ctx)
		tx, err := raw.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.ExecContext(ctx, `SELECT set_config('captain.suppress_session_change', 'on', true)`)
		Expect(err).NotTo(HaveOccurred())
		run := uuid.New()
		_, err = tx.ExecContext(ctx, `INSERT INTO captain_prompt_runs (id, session_id, root_session_id) VALUES ($1, $2, $2)`,
			run, session)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		Expect(takeCalls(ctx)).To(Equal([]hookCall{runCall("INSERT", run, session)}))
	})

	It("rolls the Captain write back when a hook raises", func(ctx SpecContext) {
		rejecter := RowHook{Table: RowHookSessions, Function: hookRejecter, Owner: hookOwnerName}
		Expect(db.RegisterRowHook(ctx, rejecter)).To(Succeed())
		DeferCleanup(func(ctx SpecContext) { Expect(db.UnregisterRowHook(ctx, rejecter)).To(Succeed()) })

		id := uuid.New()
		Expect(exec(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'gavel')`, id)).
			To(MatchError(ContainSubstring("host projection refused INSERT " + id.String())))
		var count int
		Expect(raw.QueryRowContext(ctx, `SELECT count(*) FROM captain_sessions WHERE id = $1`, id).Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
	})

	It("keeps one registration per hook when a host registers again and stops calling it once unregistered", func(ctx SpecContext) {
		hook := recorder(RowHookSessions)
		Expect(db.RegisterRowHook(ctx, hook)).To(Succeed())
		Expect(db.RegisterRowHook(ctx, RowHook{Table: hook.Table, Function: "host_record_row" + RowHookSignature, Owner: "acme-renamed"})).To(Succeed())
		var registrations []string
		rows, err := raw.QueryContext(ctx, `SELECT target_table || ' -> ' || hook_function || ' (' || owner || ')' FROM captain_row_hooks`)
		Expect(err).NotTo(HaveOccurred())
		for rows.Next() {
			var registration string
			Expect(rows.Scan(&registration)).To(Succeed())
			registrations = append(registrations, registration)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(rows.Close()).To(Succeed())
		Expect(registrations).To(Equal([]string{
			"captain_sessions -> public.host_record_row(text, text, uuid, uuid, uuid, uuid) (acme-renamed)",
		}))

		Expect(db.UnregisterRowHook(ctx, hook)).To(Succeed())
		newSession(ctx)
		Expect(takeCalls(ctx)).To(BeEmpty())
		Expect(db.UnregisterRowHook(ctx, hook)).To(MatchError(ContainSubstring("is not registered")))
	})

	DescribeTable("refuses a hook it could not call",
		func(ctx SpecContext, hook RowHook, message string) {
			Expect(db.RegisterRowHook(ctx, hook)).To(MatchError(ContainSubstring(message)))
		},
		Entry("a Captain table without hook support",
			RowHook{Table: "captain_messages", Function: hookRecorder, Owner: hookOwnerName},
			`unsupported row hook table "captain_messages"`),
		Entry("a missing function",
			RowHook{Table: RowHookSessions, Function: "public.no_such_hook" + RowHookSignature, Owner: hookOwnerName},
			`function "public.no_such_hook(text, text, uuid, uuid, uuid, uuid)" does not exist`),
		Entry("a function with other arguments",
			RowHook{Table: RowHookSessions, Function: "public.host_narrow_row(text, text)", Owner: hookOwnerName},
			`must take (text, text, uuid, uuid, uuid, uuid)`),
		Entry("a function that returns a value",
			RowHook{Table: RowHookSessions, Function: "public.host_counting_row" + RowHookSignature, Owner: hookOwnerName},
			`must return void, not integer`),
		Entry("no owner",
			RowHook{Table: RowHookSessions, Function: hookRecorder},
			"owner is required"),
	)
})
