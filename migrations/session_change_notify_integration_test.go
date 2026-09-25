package migrations

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const sessionChangeChannel = "captain_session_change"

// quietWindow is how long a listener waits for a notification that must not
// arrive. Notifications are delivered on commit, so a committed write is seen
// well inside it.
const quietWindow = 300 * time.Millisecond

var _ = Describe("session change notifications", Ordered, func() {
	var (
		dsn      string
		db       *sql.DB
		listener *pgx.Conn
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_change_notify"})
		dsn, db = handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())
	})

	BeforeEach(func(ctx SpecContext) {
		var err error
		listener, err = pgx.Connect(ctx, dsn)
		Expect(err).NotTo(HaveOccurred())
		_, err = listener.Exec(ctx, "LISTEN "+sessionChangeChannel)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(listener.Close(context.Background())).To(Succeed()) })
	})

	insertSession := func(ctx context.Context) uuid.UUID {
		id := uuid.New()
		_, err := db.ExecContext(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, 'claude')`, id)
		Expect(err).NotTo(HaveOccurred())
		return id
	}

	It("installs the notify triggers and functions", func(ctx SpecContext) {
		var triggers []string
		rows, err := db.QueryContext(ctx, `
			SELECT tgname FROM pg_trigger
			WHERE tgname IN (
				'captain_sessions_change_notify_after',
				'captain_sessions_child_notify_after',
				'captain_plans_change_notify_after',
				'captain_plan_revisions_change_notify_after'
			)
			ORDER BY tgname`)
		Expect(err).NotTo(HaveOccurred())
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			triggers = append(triggers, name)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(triggers).To(Equal([]string{
			"captain_plan_revisions_change_notify_after",
			"captain_plans_change_notify_after",
			"captain_sessions_change_notify_after",
			"captain_sessions_child_notify_after",
		}))

		var touchSource string
		Expect(db.QueryRowContext(ctx,
			`SELECT prosrc FROM pg_proc WHERE proname = 'captain_touch_session_activity'`).Scan(&touchSource)).To(Succeed())
		Expect(touchSource).To(ContainSubstring("pg_notify('captain_session_change', session_id_value::text)"))

		for _, function := range []string{
			"captain_notify_session_change", "captain_notify_plan_change", "captain_notify_plan_revision_change",
		} {
			var publicExecute bool
			Expect(db.QueryRowContext(ctx, `
				SELECT has_function_privilege('public', p.oid, 'EXECUTE')
				FROM pg_proc p WHERE p.proname = $1`, function).Scan(&publicExecute)).To(Succeed(), function)
			Expect(publicExecute).To(BeFalse(), function)
		}
	})

	It("wakes a listener for a committed message and not for a rolled-back one", func(ctx SpecContext) {
		committed, rolledBack := insertSession(ctx), insertSession(ctx)
		drainNotifications(ctx, listener)

		tx, err := db.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.ExecContext(ctx, `
			INSERT INTO captain_messages (session_id, sequence, role, parts, occurred_at)
			VALUES ($1, 0, 'user', '[]'::jsonb, now())`, rolledBack)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Rollback()).To(Succeed())

		_, err = db.ExecContext(ctx, `
			INSERT INTO captain_messages (session_id, sequence, role, parts, occurred_at)
			VALUES ($1, 0, 'user', '[]'::jsonb, now())`, committed)
		Expect(err).NotTo(HaveOccurred())

		Expect(drainNotifications(ctx, listener)).To(Equal([]string{committed.String()}))
	})

	It("notifies a message enriched in place without a newer occurred_at", func(ctx SpecContext) {
		sessionID := insertSession(ctx)
		var messageID uuid.UUID
		Expect(db.QueryRowContext(ctx, `
			INSERT INTO captain_messages (session_id, sequence, role, parts, occurred_at)
			VALUES ($1, 0, 'assistant', '[]'::jsonb, now()) RETURNING id`, sessionID).Scan(&messageID)).To(Succeed())
		drainNotifications(ctx, listener)

		_, err := db.ExecContext(ctx,
			`UPDATE captain_messages SET parts = '[{"type":"text","text":"tool output"}]'::jsonb WHERE id = $1`, messageID)
		Expect(err).NotTo(HaveOccurred())

		Expect(drainNotifications(ctx, listener)).To(Equal([]string{sessionID.String()}))
	})

	It("folds a 1,000-row ingest transaction into one notification", func(ctx SpecContext) {
		sessionID := insertSession(ctx)
		drainNotifications(ctx, listener)

		_, err := db.ExecContext(ctx, `
			INSERT INTO captain_messages (session_id, sequence, role, parts, occurred_at)
			SELECT $1, n, 'assistant', '[]'::jsonb, now() + n * interval '1 millisecond'
			FROM generate_series(0, 999) AS n`, sessionID)
		Expect(err).NotTo(HaveOccurred())

		Expect(drainNotifications(ctx, listener)).To(Equal([]string{sessionID.String()}))
	})

	It("stays silent while captain.suppress_session_change is on", func(ctx SpecContext) {
		sessionID := insertSession(ctx)
		drainNotifications(ctx, listener)

		tx, err := db.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.ExecContext(ctx, `SELECT set_config('captain.suppress_session_change', 'on', true)`)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.ExecContext(ctx, `
			INSERT INTO captain_messages (session_id, sequence, role, parts, occurred_at)
			VALUES ($1, 0, 'user', '[]'::jsonb, now())`, sessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		Expect(drainNotifications(ctx, listener)).To(BeEmpty())
	})

	It("notifies a state_version bump but not an activity touch that leaves it unchanged", func(ctx SpecContext) {
		sessionID := insertSession(ctx)
		drainNotifications(ctx, listener)

		_, err := db.ExecContext(ctx, `UPDATE captain_sessions SET last_activity_at = now() WHERE id = $1`, sessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(drainNotifications(ctx, listener)).To(BeEmpty())

		_, err = db.ExecContext(ctx, `UPDATE captain_sessions SET lifecycle_status = 'running' WHERE id = $1`, sessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(drainNotifications(ctx, listener)).To(Equal([]string{sessionID.String()}))
	})

	// Ingest projects a session's plan, todos and changed files into metadata,
	// and a follower re-reads those facets only when it is woken.
	It("notifies a metadata change but not a write that leaves metadata unchanged", func(ctx SpecContext) {
		sessionID := insertSession(ctx)
		drainNotifications(ctx, listener)

		_, err := db.ExecContext(ctx,
			`UPDATE captain_sessions SET metadata = metadata || '{"plan":{"content":"step one"}}'::jsonb WHERE id = $1`, sessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(drainNotifications(ctx, listener)).To(Equal([]string{sessionID.String()}))

		_, err = db.ExecContext(ctx,
			`UPDATE captain_sessions SET metadata = metadata || '{"plan":{"content":"step one"}}'::jsonb WHERE id = $1`, sessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(drainNotifications(ctx, listener)).To(BeEmpty())
	})

	// A follower re-resolves its thread when its session is notified, which is
	// how it discovers a transcript row or sub-agent created after it started.
	It("notifies the parent when a child session is inserted", func(ctx SpecContext) {
		parentID := insertSession(ctx)
		drainNotifications(ctx, listener)

		_, err := db.ExecContext(ctx, `
			INSERT INTO captain_sessions (id, source, parent_session_id, root_session_id, parent_relation)
			VALUES ($1, 'claude', $2, $2, 'transcript')`, uuid.New(), parentID)
		Expect(err).NotTo(HaveOccurred())

		Expect(drainNotifications(ctx, listener)).To(Equal([]string{parentID.String()}))
	})

	It("notifies a captain_plans insert and update", func(ctx SpecContext) {
		sessionID := insertSession(ctx)
		drainNotifications(ctx, listener)

		var planID uuid.UUID
		Expect(db.QueryRowContext(ctx, `
			INSERT INTO captain_plans (source_session_id, title) VALUES ($1, 'draft') RETURNING id`,
			sessionID).Scan(&planID)).To(Succeed())
		Expect(drainNotifications(ctx, listener)).To(Equal([]string{sessionID.String()}))

		_, err := db.ExecContext(ctx, `UPDATE captain_plans SET title = 'revised' WHERE id = $1`, planID)
		Expect(err).NotTo(HaveOccurred())
		Expect(drainNotifications(ctx, listener)).To(Equal([]string{sessionID.String()}))
	})

	// Appending a revision is how a plan's content changes, and it writes only
	// captain_plan_revisions.
	It("notifies the plan's session when a revision is appended", func(ctx SpecContext) {
		sessionID := insertSession(ctx)
		var planID uuid.UUID
		Expect(db.QueryRowContext(ctx, `
			INSERT INTO captain_plans (source_session_id, title) VALUES ($1, 'draft') RETURNING id`,
			sessionID).Scan(&planID)).To(Succeed())
		drainNotifications(ctx, listener)

		_, err := db.ExecContext(ctx, `
			INSERT INTO captain_plan_revisions (plan_id, revision, plan_markdown, content_hash)
			VALUES ($1, 1, '# step one', 'hash-1')`, planID)
		Expect(err).NotTo(HaveOccurred())
		Expect(drainNotifications(ctx, listener)).To(Equal([]string{sessionID.String()}))
	})
})

// drainNotifications returns every payload the listener receives until the
// channel has been quiet for quietWindow.
func drainNotifications(ctx context.Context, listener *pgx.Conn) []string {
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
		Expect(notification.Channel).To(Equal(sessionChangeChannel))
		payloads = append(payloads, notification.Payload)
	}
}
