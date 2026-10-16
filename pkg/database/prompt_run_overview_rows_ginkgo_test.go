package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// overviewOutOfSync lists the runs whose trigger-maintained row differs from
// what captain_prompt_run_overview_rebuild computes, in either direction.
const overviewOutOfSync = `
	SELECT d.id::text FROM (
	  (SELECT * FROM captain_prompt_run_overview_rows
	   EXCEPT ALL SELECT * FROM captain_prompt_run_overview_rebuild())
	  UNION ALL
	  (SELECT * FROM captain_prompt_run_overview_rebuild()
	   EXCEPT ALL SELECT * FROM captain_prompt_run_overview_rows)
	) d ORDER BY 1`

type overviewUsage struct {
	ModelCallCount int64
	TotalTokens    int64
	CostUSD        string
	PlanCount      int64
	PendingCount   int64
	IterationCount int64
	LatestState    sql.NullString
	VerifyOnly     bool
	PlanMode       bool
}

var _ = Describe("captain_prompt_run_overview_rows", Ordered, func() {
	var (
		db  *DB
		raw *sql.DB
		dsn string
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_prompt_run_overview_rows"})
		raw, dsn = handle.SQL(), handle.DSN()
		var err error
		db, err = Open(ctx, WithDSN(dsn), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	exec := func(ctx context.Context, query string, args ...any) {
		GinkgoHelper()
		_, err := raw.ExecContext(ctx, query, args...)
		Expect(err).NotTo(HaveOccurred(), query)
	}
	expectInSync := func(ctx context.Context, step string, wantRows int) {
		GinkgoHelper()
		var diverged []string
		rows, err := raw.QueryContext(ctx, overviewOutOfSync)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		for rows.Next() {
			var id string
			Expect(rows.Scan(&id)).To(Succeed())
			diverged = append(diverged, id)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(diverged).To(BeEmpty(), "after %s", step)
		var count int
		Expect(raw.QueryRowContext(ctx, `SELECT count(*) FROM captain_prompt_run_overview_rows`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(wantRows), "rows after %s", step)
	}
	usage := func(ctx context.Context, run uuid.UUID) overviewUsage {
		GinkgoHelper()
		var got overviewUsage
		Expect(raw.QueryRowContext(ctx, `
			SELECT o.model_call_count, o.total_tokens, o.cost_usd::text, o.plan_count,
			  r.pending_turn_request_count, o.iteration_count, o.latest_iteration_state::text,
			  r.verify_only, r.plan_mode
			FROM captain_prompt_run_overview o
			JOIN captain_prompt_run_overview_rows r ON r.id = o.id
			WHERE o.id = $1`, run).Scan(
			&got.ModelCallCount, &got.TotalTokens, &got.CostUSD, &got.PlanCount,
			&got.PendingCount, &got.IterationCount, &got.LatestState, &got.VerifyOnly, &got.PlanMode,
		)).To(Succeed())
		return got
	}
	seedRun := func(ctx context.Context, spec string) (session, turn, run uuid.UUID) {
		GinkgoHelper()
		session, turn, run = uuid.New(), uuid.New(), uuid.New()
		exec(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'claude')`, session)
		exec(ctx, `INSERT INTO captain_turns (id, session_id, turn_index, status) VALUES ($1, $2, 0, 'ended')`, turn, session)
		exec(ctx, `INSERT INTO captain_prompt_runs (id, session_id, root_session_id, rendered_spec)
			VALUES ($1, $2, $2, $3::jsonb)`, run, session, spec)
		return session, turn, run
	}

	It("keeps every row equal to the rebuild through a run's whole lifecycle", func(ctx SpecContext) {
		session, turn, run := seedRun(ctx, `{"workflow":{"verify":{}},"permissions":{"mode":"plan"}}`)
		_, otherTurn, other := seedRun(ctx, `{"prompt":{"user":"do it"}}`)
		expectInSync(ctx, "creating runs", 2)
		Expect(usage(ctx, run)).To(Equal(overviewUsage{CostUSD: "0", VerifyOnly: true, PlanMode: true}))

		exec(ctx, `INSERT INTO captain_prompt_run_iterations (prompt_run_id, iteration, state)
			VALUES ($1, 0, 'failed'), ($1, 1, 'running')`, run)
		exec(ctx, `UPDATE captain_prompt_run_iterations SET state = 'succeeded', verification_result = '{"ok":true}'
			WHERE prompt_run_id = $1 AND iteration = 1`, run)
		expectInSync(ctx, "adding iterations", 2)

		exec(ctx, `INSERT INTO captain_model_calls
			  (turn_id, call_index, prompt_run_id, model, input_tokens, output_tokens, reasoning_tokens,
			   cache_read_tokens, cache_write_tokens, context_tokens, context_window_tokens,
			   input_cost, output_cost, reasoning_cost, cache_read_cost, cache_write_cost, provider_cost_usd, currency)
			VALUES
			  ($1, 0, $2, 'm', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'USD'),
			  ($1, 1, $2, 'm', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0.25, 'usd'),
			  ($1, 2, $2, 'm', 100, 0, 0, 0, 0, 0, 0, 9, 9, 9, 9, 9, 9, 'EUR'),
			  ($3, 0, $4, 'm', 7, 0, 0, 0, 0, 0, 0, 0.5, 0, 0, 0, 0, 0, 'USD')`, turn, run, otherTurn, other)
		for i := 1; i <= 5; i++ {
			exec(ctx, `UPDATE captain_model_calls SET input_tokens = input_tokens + 10, output_tokens = output_tokens + 2,
				input_cost = input_cost + 0.01 WHERE turn_id = $1 AND call_index = 0`, turn)
		}
		exec(ctx, `UPDATE captain_model_calls SET reasoning_tokens = reasoning_tokens + 1 WHERE turn_id IN ($1, $2)`, turn, otherTurn)
		expectInSync(ctx, "streaming model-call usage", 2)
		Expect(usage(ctx, run)).To(Equal(overviewUsage{
			ModelCallCount: 3, TotalTokens: 163, CostUSD: "0.30000000", IterationCount: 2,
			LatestState: sql.NullString{String: "succeeded", Valid: true}, VerifyOnly: true, PlanMode: true,
		}))

		exec(ctx, `UPDATE captain_model_calls SET prompt_run_id = $2 WHERE turn_id = $1 AND call_index = 1`, turn, other)
		exec(ctx, `UPDATE captain_model_calls SET currency = 'USD' WHERE turn_id = $1 AND call_index = 2`, turn)
		expectInSync(ctx, "moving a call and changing its currency", 2)

		var plan uuid.UUID
		Expect(raw.QueryRowContext(ctx, `INSERT INTO captain_plans (source_session_id, source_prompt_run_id, title)
			VALUES ($1, $2, 'plan') RETURNING id`, session, run).Scan(&plan)).To(Succeed())
		exec(ctx, `INSERT INTO captain_plan_revisions (plan_id, revision, plan_markdown, content_hash)
			VALUES ($1, 1, 'one', 'h1'), ($1, 2, 'two', 'h2')`, plan)
		exec(ctx, `UPDATE captain_plans SET approval_state = 'approved' WHERE id = $1`, plan)
		expectInSync(ctx, "adding a plan and its revisions", 2)

		var request uuid.UUID
		Expect(raw.QueryRowContext(ctx, `INSERT INTO captain_turn_requests (session_id, prompt_run_id, kind)
			VALUES ($1, $2, 'question') RETURNING id`, session, run).Scan(&request)).To(Succeed())
		expectInSync(ctx, "raising a turn request", 2)
		Expect(usage(ctx, run).PendingCount).To(Equal(int64(1)))
		exec(ctx, `UPDATE captain_turn_requests SET state = 'answered' WHERE id = $1`, request)
		expectInSync(ctx, "resolving the turn request", 2)
		Expect(usage(ctx, run).PendingCount).To(BeZero())

		exec(ctx, `DELETE FROM captain_prompt_run_iterations WHERE prompt_run_id = $1 AND iteration = 1`, run)
		exec(ctx, `UPDATE captain_prompt_runs SET state = 'succeeded', phase = 'finished' WHERE id = $1`, run)
		expectInSync(ctx, "deleting the latest iteration and finishing the run", 2)
		Expect(usage(ctx, run).LatestState).To(Equal(sql.NullString{String: "failed", Valid: true}))

		exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, session)
		expectInSync(ctx, "deleting the run with its session", 1)
	})

	It("recomputes a truncated run's row on its next delta and ensure rebuilds the rest", func(ctx SpecContext) {
		_, turn, run := seedRun(ctx, `{}`)
		exec(ctx, `INSERT INTO captain_model_calls (turn_id, call_index, prompt_run_id, model, input_tokens)
			VALUES ($1, 0, $2, 'm', 5)`, turn, run)
		exec(ctx, `TRUNCATE captain_prompt_run_overview_rows`)

		exec(ctx, `UPDATE captain_model_calls SET input_tokens = 6 WHERE turn_id = $1`, turn)
		var healed int
		Expect(raw.QueryRowContext(ctx, `
			SELECT count(*) FROM (
			  (SELECT * FROM captain_prompt_run_overview_rows
			   EXCEPT ALL SELECT * FROM captain_prompt_run_overview_rebuild(ARRAY[$1::uuid]))
			  UNION ALL
			  (SELECT * FROM captain_prompt_run_overview_rebuild(ARRAY[$1::uuid])
			   EXCEPT ALL SELECT * FROM captain_prompt_run_overview_rows)
			) d`, run).Scan(&healed)).To(Succeed())
		Expect(healed).To(BeZero(), "the touched run's row is recomputed exactly and is the only row")
		Expect(usage(ctx, run).TotalTokens).To(Equal(int64(6)))

		rebuilt, err := db.EnsurePromptRunOverview(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(rebuilt).To(BeTrue())
		expectInSync(ctx, "ensure after truncate", 2)
		rebuilt, err = db.EnsurePromptRunOverview(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(rebuilt).To(BeFalse(), "a populated table is left alone")
	})

	It("keeps its rows when the migration bundle is applied again or 89 re-runs", func(ctx SpecContext) {
		Expect(Migrate(ctx, dsn)).To(Succeed(), "Atlas plans a drop for the undeclared table; the bundle must filter it")
		expectInSync(ctx, "a steady-state apply", 2)

		exec(ctx, `DELETE FROM schema_migration_scripts WHERE path = '89_prompt_run_overview_rows.sql'`)
		Expect(Migrate(ctx, dsn)).To(Succeed())
		expectInSync(ctx, "re-running 89", 2)
	})

	It("notifies a real change of a run's row and stays silent for a no-op write", func(ctx SpecContext) {
		_, turn, run := seedRun(ctx, `{}`)
		exec(ctx, `INSERT INTO captain_model_calls (turn_id, call_index, prompt_run_id, model) VALUES ($1, 0, $2, 'm')`, turn, run)
		exec(ctx, `INSERT INTO captain_prompt_run_iterations (prompt_run_id, iteration) VALUES ($1, 0)`, run)
		notifications := listenOverview(ctx, db)

		exec(ctx, `UPDATE captain_model_calls SET status = status WHERE turn_id = $1`, turn)
		exec(ctx, `UPDATE captain_prompt_run_iterations SET request = request WHERE prompt_run_id = $1`, run)
		Consistently(notifications, rowChangeQuiet).ShouldNot(Receive())

		exec(ctx, `UPDATE captain_model_calls SET output_tokens = 3 WHERE turn_id = $1`, turn)
		Eventually(notifications, 5*time.Second).Should(Receive(Equal(run.String())))
	})

	It("reads 50 runs by id from the table without re-aggregating their children", func(ctx SpecContext) {
		ids := seedOverviewLoad(ctx, raw, 1000, 30)
		legacy, err := os.ReadFile("../../migrations/69_view_prompt_run_overview.sql")
		Expect(err).NotTo(HaveOccurred())
		exec(ctx, strings.ReplaceAll(string(legacy), "public.captain_prompt_run_overview", "public.captain_prompt_run_overview_legacy"))
		exec(ctx, `ANALYZE`)

		before := explainOverviewRead(ctx, raw, "captain_prompt_run_overview_legacy", ids[:50])
		after := explainOverviewRead(ctx, raw, "captain_prompt_run_overview", ids[:50])
		GinkgoWriter.Printf("legacy view: %d buffers, %.3f ms\ntable view: %d buffers, %.3f ms\n",
			before.Buffers, before.Milliseconds, after.Buffers, after.Milliseconds)
		Expect(before.Relations).To(ContainElements("captain_model_calls", "captain_prompt_run_iterations", "captain_plans"))
		Expect(after.Relations).To(Equal([]string{"captain_prompt_run_overview_rows"}))
		Expect(after.Buffers).To(BeNumerically("<", before.Buffers))
	})
})

func listenOverview(ctx context.Context, db *DB) <-chan string {
	GinkgoHelper()
	pool, err := db.Gorm().DB()
	Expect(err).NotTo(HaveOccurred())
	listener, err := Listen(ctx, pool, PromptRunOverviewChannel)
	Expect(err).NotTo(HaveOccurred())
	notifications := make(chan string, 64)
	listenCtx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() {
		stopped <- listener.Run(listenCtx, func(payload string) error { notifications <- payload; return nil }, func() error { return nil })
	}()
	DeferCleanup(func() {
		cancel()
		Eventually(stopped, 10*time.Second).Should(Receive(MatchError(context.Canceled)))
	})
	return notifications
}

// seedOverviewLoad inserts runs finished runs, each with two iterations and
// callsPerRun model calls, and returns their ids.
func seedOverviewLoad(ctx context.Context, raw *sql.DB, runs, callsPerRun int) []uuid.UUID {
	GinkgoHelper()
	session, turn := uuid.New(), uuid.New()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO captain_sessions (id, source) VALUES ($1, 'claude')`, []any{session}},
		{`INSERT INTO captain_turns (id, session_id, turn_index, status) VALUES ($1, $2, 0, 'ended')`, []any{turn, session}},
		{`INSERT INTO captain_prompt_runs (session_id, root_session_id, state, phase)
		 SELECT $1, $1, 'succeeded', 'finished' FROM generate_series(1, $2::int)`, []any{session, runs}},
		{`INSERT INTO captain_prompt_run_iterations (prompt_run_id, iteration, state)
		 SELECT r.id, i, 'succeeded' FROM captain_prompt_runs r, generate_series(0, 1) i WHERE r.session_id = $1`, []any{session}},
		{`INSERT INTO captain_model_calls (turn_id, call_index, prompt_run_id, model, input_tokens, input_cost)
		 SELECT $1, row_number() OVER (), r.id, 'm', 10, 0.01
		 FROM captain_prompt_runs r, generate_series(1, $3::int) WHERE r.session_id = $2`, []any{turn, session, callsPerRun}},
	} {
		_, err := raw.ExecContext(ctx, statement.query, statement.args...)
		Expect(err).NotTo(HaveOccurred(), statement.query)
	}
	rows, err := raw.QueryContext(ctx, `SELECT id FROM captain_prompt_runs WHERE session_id = $1 ORDER BY id`, session)
	Expect(err).NotTo(HaveOccurred())
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		Expect(rows.Scan(&id)).To(Succeed())
		ids = append(ids, id)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return ids
}

type overviewReadCost struct {
	Buffers      int64
	Milliseconds float64
	Relations    []string
}

type explainNode struct {
	Relation   string        `json:"Relation Name"`
	SharedHit  int64         `json:"Shared Hit Blocks"`
	SharedRead int64         `json:"Shared Read Blocks"`
	Plans      []explainNode `json:"Plans"`
}

func (n explainNode) relations() []string {
	var names []string
	if n.Relation != "" {
		names = append(names, n.Relation)
	}
	for _, child := range n.Plans {
		for _, name := range child.relations() {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

func explainOverviewRead(ctx context.Context, raw *sql.DB, view string, ids []uuid.UUID) overviewReadCost {
	GinkgoHelper()
	args := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id.String()
	}
	var plan string
	Expect(raw.QueryRowContext(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT * FROM `+view+
		` WHERE id = ANY($1::uuid[])`, "{"+strings.Join(args, ",")+"}").Scan(&plan)).To(Succeed())
	var parsed []struct {
		Plan          explainNode `json:"Plan"`
		ExecutionTime float64     `json:"Execution Time"`
	}
	Expect(json.Unmarshal([]byte(plan), &parsed)).To(Succeed())
	Expect(parsed).To(HaveLen(1))
	top := parsed[0].Plan
	return overviewReadCost{
		Buffers: top.SharedHit + top.SharedRead, Milliseconds: parsed[0].ExecutionTime, Relations: top.relations(),
	}
}
