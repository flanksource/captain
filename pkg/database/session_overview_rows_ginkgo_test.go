package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Session overview rows", func() {
	It("lists only live sessions' directories, excluding auto-review sessions", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_live_session_dirs"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		startedAt := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
		create := func(name, cwd string, pid int64) uuid.UUID {
			GinkgoHelper()
			row, err := db.CreateOrGetSession(ctx, CreateSessionInput{
				Source: "codex", Provider: "openai", HostID: "live-dirs", ProviderSessionID: name, CWD: cwd,
			})
			Expect(err).NotTo(HaveOccurred())
			if pid > 0 {
				Expect(db.UpsertSessionProcess(ctx, SessionProcessInput{
					SessionID: row.ID, HostID: "live-dirs", BootID: "boot", PID: pid,
					ProcessStartedAt: startedAt, Status: "active", CWD: cwd + "/worktree", Source: "codex",
				})).To(Succeed())
			}
			return row.ID
		}
		live := create("live", "/work/live", 101)
		ended := create("ended", "/work/ended", 102)
		create("idle", "/work/idle", 0)
		review := create("review", "/work/review", 103)
		Expect(db.Gorm().Exec("UPDATE captain_session_processes SET ended_at = ? WHERE session_id = ?", startedAt.Add(time.Minute), ended).Error).To(Succeed())
		Expect(db.Gorm().Exec("UPDATE captain_sessions SET metadata = jsonb_build_object('model', ?::text) WHERE id = ?", api.CodexAutoReviewModel, review).Error).To(Succeed())

		dirs, err := db.ListLiveSessionDirs(ctx)

		Expect(err).NotTo(HaveOccurred())
		cwd, processCWD := "/work/live", "/work/live/worktree"
		Expect(dirs).To(Equal([]LiveSessionDir{{SessionID: live, CWD: &cwd, ProcessCWD: &processCWD}}))
	})

	It("refills the overview after crash recovery truncates it", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_overview_ensure"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		Expect(db.Gorm().Exec(`INSERT INTO captain_sessions (source, initial_prompt)
			SELECT 'claude', 'prompt ' || n FROM generate_series(1, ?) AS n`, sessionOverviewEnsureBatch+1).Error).To(Succeed())
		before, err := db.ListSessionSummaries(ctx, SessionListFilter{IncludeTotal: true, Limit: 1})
		Expect(err).NotTo(HaveOccurred())

		rebuilt, err := db.EnsureSessionOverview(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(rebuilt).To(BeFalse(), "every session already has a row")

		Expect(db.Gorm().Exec("TRUNCATE captain_session_overview_rows").Error).To(Succeed())
		rebuilt, err = db.EnsureSessionOverview(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(rebuilt).To(BeTrue())

		after, err := db.ListSessionSummaries(ctx, SessionListFilter{IncludeTotal: true, Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
		Expect(after.Total).To(Equal(int64(sessionOverviewEnsureBatch + 1)))
	})

	It("pages the list and live lookups through the overview indexes", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_list_plan"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		const sessions, liveSessions = 20000, 20
		Expect(db.Gorm().Exec(`INSERT INTO captain_sessions (source, provider_session_id, cwd, initial_prompt, started_at, last_activity_at)
			SELECT CASE WHEN n % 2 = 0 THEN 'claude' ELSE 'codex' END, 'seed-' || n, '/work/project-' || (n % 50),
				CASE WHEN n % 4 = 0 THEN NULL ELSE 'prompt ' || n END,
				now() - n * interval '1 minute', now() - n * interval '1 minute'
			FROM generate_series(1, ?) AS n`, sessions).Error).To(Succeed())
		Expect(db.Gorm().Exec(`INSERT INTO captain_session_processes (session_id, host_id, boot_id, pid, process_started_at, status)
			SELECT id, 'host', 'boot', row_number() OVER (ORDER BY id), now(), 'active'
			FROM captain_sessions ORDER BY id LIMIT ?`, liveSessions).Error).To(Succeed())
		Expect(db.Gorm().Exec("ANALYZE captain_session_overview_rows").Error).To(Succeed())

		explainSQL := func(query string, args ...any) string {
			GinkgoHelper()
			rows, err := db.Gorm().Raw("EXPLAIN (ANALYZE, BUFFERS) "+query, args...).Rows()
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()
			var plan []string
			for rows.Next() {
				var line string
				Expect(rows.Scan(&line)).To(Succeed())
				plan = append(plan, line)
			}
			Expect(rows.Err()).NotTo(HaveOccurred())
			return strings.Join(plan, "\n")
		}
		explain := func(filter SessionListFilter) string {
			GinkgoHelper()
			limit, err := sessionListLimit(filter.Limit)
			Expect(err).NotTo(HaveOccurred())
			return explainSQL(sessionListQuery(filter.IncludeTotal), sessionListArgs(filter, nil, limit)...)
		}
		for name, filter := range map[string]SessionListFilter{
			"sessions tab":    {ExcludeSubagents: true, ExcludeInactiveEmpty: true, Limit: 200},
			"live sessions":   {LiveOnly: true, Limit: 100},
			"cli with total":  {RootsOnly: true, IncludeTotal: true, Limit: 50},
			"substring query": {Query: "prompt 1999", Limit: 50},
		} {
			plan := explain(filter)
			fmt.Fprintf(GinkgoWriter, "--- %s ---\n%s\n", name, plan)
			if !filter.IncludeTotal && filter.Query == "" {
				Expect(plan).NotTo(ContainSubstring("Seq Scan"), name)
				Expect(plan).To(ContainSubstring("captain_session_overview_rows_"), name)
			}
		}

		// A todo's session tab resolves each attempt by provider session id
		// (ListSessionOverviewsByProviderSessionID) and its launcher's transcript
		// child (ListTranscriptChildOverviews). Both read the overview rows, which
		// unlike captain_sessions carried no index for either predicate.
		var parentID uuid.UUID
		Expect(db.Gorm().Raw(`SELECT id FROM captain_sessions WHERE provider_session_id = 'seed-1'`).Row().Scan(&parentID)).To(Succeed())
		Expect(db.Gorm().Exec(`UPDATE captain_sessions SET parent_session_id = ?, root_session_id = ?, parent_relation = ?
			WHERE provider_session_id = 'seed-2'`, parentID, parentID, SessionParentRelationTranscript).Error).To(Succeed())
		Expect(db.Gorm().Exec("ANALYZE captain_session_overview_rows").Error).To(Succeed())
		for name, lookup := range map[string]struct {
			query string
			args  []any
			index string
		}{
			"provider session id": {
				query: `SELECT id FROM captain_session_overview WHERE provider_session_id = ?`,
				args:  []any{"seed-1999"}, index: "captain_session_overview_rows_provider_session_id_idx",
			},
			"transcript children": {
				query: `SELECT id FROM captain_session_overview WHERE parent_session_id IN ? AND parent_relation = ?`,
				args:  []any{[]uuid.UUID{parentID}, SessionParentRelationTranscript}, index: "captain_session_overview_rows_parent_session_id_idx",
			},
		} {
			plan := explainSQL(lookup.query, lookup.args...)
			fmt.Fprintf(GinkgoWriter, "--- %s ---\n%s\n", name, plan)
			Expect(plan).NotTo(ContainSubstring("Seq Scan"), name)
			Expect(plan).To(ContainSubstring(lookup.index), name)
		}

		session, run := measureOverviewCommits(ctx, db)
		var drift int64
		Expect(db.Gorm().Raw(`SELECT
			(SELECT count(*) FROM (SELECT id, message_count, tool_call_count, model_call_count, total_tokens, cost_usd
				FROM captain_session_overview_rows WHERE id = @session
				EXCEPT SELECT id, message_count, tool_call_count, model_call_count, total_tokens, cost_usd
				FROM captain_session_overview_compute WHERE id = @session) d)
			+ (SELECT count(*) FROM (SELECT * FROM captain_prompt_run_overview_rows WHERE id = @run
				EXCEPT SELECT * FROM captain_prompt_run_overview_rebuild() WHERE id = @run) d)`,
			sql.Named("session", session), sql.Named("run", run)).Scan(&drift).Error).To(Succeed())
		Expect(drift).To(BeZero(), "both commits left their overview rows exact")
	})
})

// measureOverviewCommits seeds one heavy session (2,000 messages, 200 model
// calls) with one run (5 iterations, 100 of those calls), then times the
// commits that recompute them: a message insert recomputes the session, and a
// streamed model-call update recomputes the run and the session.
func measureOverviewCommits(ctx context.Context, db *DB) (uuid.UUID, uuid.UUID) {
	GinkgoHelper()
	session, run, turn := uuid.New(), uuid.New(), uuid.New()
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO captain_sessions (id, source) VALUES (?, 'claude')`, []any{session}},
		{`INSERT INTO captain_prompt_runs (id, session_id, root_session_id) VALUES (?, ?, ?)`, []any{run, session, session}},
		{`INSERT INTO captain_prompt_run_iterations (prompt_run_id, iteration) SELECT ?, n FROM generate_series(0, 4) AS n`, []any{run}},
		{`INSERT INTO captain_turns (id, session_id, turn_index) VALUES (?, ?, 0)`, []any{turn, session}},
		{`INSERT INTO captain_model_calls (turn_id, call_index, model, prompt_run_id, input_tokens, output_cost)
			SELECT ?, n, 'model-a', CASE WHEN n < 100 THEN ?::uuid END, n, 0.001 FROM generate_series(0, 199) AS n`, []any{turn, run}},
		{`INSERT INTO captain_messages (session_id, sequence, role, parts)
			SELECT ?, n, 'assistant', '[{"type":"text","text":"answer"},{"type":"tool-shell","toolName":"shell","input":{"cmd":"make test"}}]'::jsonb
			FROM generate_series(0, 1999) AS n`, []any{session}},
	} {
		Expect(db.Gorm().Exec(seed.query, seed.args...).Error).To(Succeed())
	}
	timeCommit := func(name, query string, args ...any) {
		GinkgoHelper()
		tx := db.Gorm().Begin()
		Expect(tx.Error).NotTo(HaveOccurred())
		Expect(tx.Exec(query, args...).Error).To(Succeed())
		started := time.Now()
		Expect(tx.Commit().Error).To(Succeed())
		fmt.Fprintf(GinkgoWriter, "--- commit recomputing %s: %s ---\n", name, time.Since(started))
	}
	timeCommit("the session (2,000 messages, 200 calls)",
		`INSERT INTO captain_messages (session_id, sequence, role, parts) VALUES (?, 2000, 'user', '[]'::jsonb)`, session)
	timeCommit("the run (5 iterations, 100 calls) and its session",
		`UPDATE captain_model_calls SET output_tokens = output_tokens + 5 WHERE turn_id = ? AND call_index = 0`, turn)
	return session, run
}
