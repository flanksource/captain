package database

import (
	"context"
	"database/sql"
	"errors"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The host tables stand in for gavel's todo_issue_prompt_runs and
// todo_issue_plans: host-owned rows that name a Captain row without a foreign
// key onto Captain's tables.
const (
	hostRunLinks  = "public.host_run_links"
	hostPlanLinks = "public.host_plan_links"
	hostOwner     = "acme-host"
)

var _ = Describe("Captain delete guards", Ordered, func() {
	var (
		db  *DB
		raw *sql.DB
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_delete_guard"})
		var err error
		db, err = Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		// Captain's own pgx pool, so a rejection surfaces as *pgconn.PgError.
		raw, err = db.Gorm().DB()
		Expect(err).NotTo(HaveOccurred())
		Expect(Migrate(ctx, handle.DSN())).To(Succeed(), "a second apply must be idempotent")

		for _, statement := range []string{
			`CREATE TABLE ` + hostRunLinks + ` (issue_id uuid NOT NULL, prompt_run_id uuid NOT NULL)`,
			`CREATE TABLE ` + hostPlanLinks + ` (issue_id uuid NOT NULL, plan_id uuid NOT NULL, label text)`,
		} {
			_, err = raw.ExecContext(ctx, statement)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(db.RegisterDeleteGuard(ctx, DeleteGuard{
			Table: DeleteGuardPromptRuns, HostTable: hostRunLinks, HostColumn: "prompt_run_id", Owner: hostOwner,
		})).To(Succeed())
		Expect(db.RegisterDeleteGuard(ctx, DeleteGuard{
			Table: DeleteGuardPlans, HostTable: hostPlanLinks, HostColumn: "plan_id", Owner: hostOwner,
		})).To(Succeed())
	})

	exec := func(ctx context.Context, query string, args ...any) error {
		_, err := raw.ExecContext(ctx, query, args...)
		return err
	}
	newRun := func(ctx context.Context) (session, run uuid.UUID) {
		GinkgoHelper()
		session, run = uuid.New(), uuid.New()
		Expect(exec(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'gavel')`, session)).To(Succeed())
		Expect(exec(ctx, `INSERT INTO captain_prompt_runs (id, session_id, root_session_id) VALUES ($1, $2, $2)`,
			run, session)).To(Succeed())
		return session, run
	}
	expectGuarded := func(err error, table string, id uuid.UUID) {
		GinkgoHelper()
		var pgErr *pgconn.PgError
		Expect(errors.As(err, &pgErr)).To(BeTrue(), "expected a PostgreSQL error, got %v", err)
		Expect(pgErr.Code).To(Equal("23503"))
		Expect(pgErr.ConstraintName).To(Equal("captain_delete_guard"))
		Expect(pgErr.Message).To(ContainSubstring(table))
		Expect(pgErr.Message).To(ContainSubstring(id.String()))
		Expect(pgErr.Message).To(ContainSubstring(hostOwner))
	}
	countRows := func(ctx context.Context, table string, id uuid.UUID) int {
		GinkgoHelper()
		var count int
		Expect(raw.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&count)).To(Succeed())
		return count
	}

	It("rejects deleting a prompt run a host row references and allows it once the reference is gone", func(ctx SpecContext) {
		_, run := newRun(ctx)
		Expect(exec(ctx, `INSERT INTO `+hostRunLinks+` VALUES ($1, $2)`, uuid.New(), run)).To(Succeed())

		expectGuarded(exec(ctx, `DELETE FROM captain_prompt_runs WHERE id = $1`, run), "captain_prompt_runs", run)
		Expect(countRows(ctx, "captain_prompt_runs", run)).To(Equal(1))

		Expect(exec(ctx, `DELETE FROM `+hostRunLinks+` WHERE prompt_run_id = $1`, run)).To(Succeed())
		Expect(exec(ctx, `DELETE FROM captain_prompt_runs WHERE id = $1`, run)).To(Succeed())
		Expect(countRows(ctx, "captain_prompt_runs", run)).To(BeZero())
	})

	It("rejects a session delete that would cascade to a referenced prompt run", func(ctx SpecContext) {
		session, run := newRun(ctx)
		Expect(exec(ctx, `INSERT INTO `+hostRunLinks+` VALUES ($1, $2)`, uuid.New(), run)).To(Succeed())

		expectGuarded(exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, session), "captain_prompt_runs", run)
		Expect(countRows(ctx, "captain_sessions", session)).To(Equal(1))
	})

	It("allows deleting an unreferenced session aggregate", func(ctx SpecContext) {
		session, run := newRun(ctx)
		Expect(exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, session)).To(Succeed())
		Expect(countRows(ctx, "captain_prompt_runs", run)).To(BeZero())
	})

	It("rejects deleting a plan a host row references", func(ctx SpecContext) {
		session, _ := newRun(ctx)
		var plan uuid.UUID
		Expect(raw.QueryRowContext(ctx, `INSERT INTO captain_plans (source_session_id, title) VALUES ($1, 'draft') RETURNING id`,
			session).Scan(&plan)).To(Succeed())
		Expect(exec(ctx, `INSERT INTO `+hostPlanLinks+` (issue_id, plan_id) VALUES ($1, $2)`, uuid.New(), plan)).To(Succeed())

		expectGuarded(exec(ctx, `DELETE FROM captain_plans WHERE id = $1`, plan), "captain_plans", plan)
		Expect(countRows(ctx, "captain_plans", plan)).To(Equal(1))
	})

	It("keeps one registration per host column when a host registers again", func(ctx SpecContext) {
		Expect(db.RegisterDeleteGuard(ctx, DeleteGuard{
			Table: DeleteGuardPromptRuns, HostTable: "host_run_links", HostColumn: "prompt_run_id", Owner: hostOwner,
		})).To(Succeed())
		var registrations []string
		rows, err := raw.QueryContext(ctx, `SELECT target_table || ' <- ' || host_table || '.' || host_column || ' (' || owner || ')'
			FROM captain_delete_guards ORDER BY 1`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		for rows.Next() {
			var registration string
			Expect(rows.Scan(&registration)).To(Succeed())
			registrations = append(registrations, registration)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(registrations).To(Equal([]string{
			"captain_plans <- public.host_plan_links.plan_id (acme-host)",
			"captain_prompt_runs <- public.host_run_links.prompt_run_id (acme-host)",
		}))
	})

	DescribeTable("refuses a guard it could not enforce",
		func(ctx SpecContext, guard DeleteGuard, message string) {
			Expect(db.RegisterDeleteGuard(ctx, guard)).To(MatchError(ContainSubstring(message)))
		},
		Entry("a Captain table without guard support",
			DeleteGuard{Table: "captain_messages", HostTable: hostRunLinks, HostColumn: "prompt_run_id", Owner: hostOwner},
			`unsupported delete guard table "captain_messages"`),
		Entry("a missing host table",
			DeleteGuard{Table: DeleteGuardPromptRuns, HostTable: "public.no_such_links", HostColumn: "prompt_run_id", Owner: hostOwner},
			`host table "public.no_such_links" does not exist`),
		Entry("a missing host column",
			DeleteGuard{Table: DeleteGuardPromptRuns, HostTable: hostRunLinks, HostColumn: "run_id", Owner: hostOwner},
			`has no column "run_id"`),
		Entry("a host column that cannot hold a Captain id",
			DeleteGuard{Table: DeleteGuardPlans, HostTable: hostPlanLinks, HostColumn: "label", Owner: hostOwner},
			`must be uuid, not text`),
		Entry("no owner",
			DeleteGuard{Table: DeleteGuardPlans, HostTable: hostPlanLinks, HostColumn: "plan_id"},
			"owner is required"),
	)
})
