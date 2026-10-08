package database

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// rowChangeQuiet is how long a test waits for a notification that must not
// arrive. Notifications are delivered on commit, so a committed write is seen
// well inside it.
const rowChangeQuiet = 400 * time.Millisecond

var _ = Describe("captain_row_change notifications", Ordered, func() {
	var (
		dsn     string
		db      *DB
		raw     *sql.DB
		changes chan RowChange
		resyncs chan struct{}
		stop    context.CancelFunc
		stopped chan error
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_row_change_notify"})
		dsn, raw = handle.DSN(), handle.SQL()
		var err error
		db, err = Open(ctx, WithDSN(dsn), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		Expect(Migrate(ctx, dsn)).To(Succeed(), "a second apply must be idempotent")
	})

	BeforeEach(func() {
		changes, resyncs, stopped = make(chan RowChange, 256), make(chan struct{}, 4), make(chan error, 1)
		listenCtx, cancel := context.WithCancel(context.Background())
		stop = cancel
		go func() {
			stopped <- db.ListenRowChanges(listenCtx, RowChangeListener{
				OnChange: func(_ context.Context, change RowChange) error { changes <- change; return nil },
				OnResync: func(context.Context) error { resyncs <- struct{}{}; return nil },
			})
		}()
		Eventually(resyncs, 10*time.Second).Should(Receive(), "the first resync signals an established LISTEN")
		DeferCleanup(func() {
			stop()
			Eventually(stopped, 10*time.Second).Should(Receive(MatchError(context.Canceled)))
		})
	})

	exec := func(ctx context.Context, query string, args ...any) {
		GinkgoHelper()
		_, err := raw.ExecContext(ctx, query, args...)
		Expect(err).NotTo(HaveOccurred())
	}
	drain := func() []RowChange {
		GinkgoHelper()
		var received []RowChange
		for {
			select {
			case change := <-changes:
				received = append(received, change)
			case <-time.After(rowChangeQuiet):
				return received
			}
		}
	}
	insertSession := func(ctx context.Context, parent *uuid.UUID) uuid.UUID {
		GinkgoHelper()
		id := uuid.New()
		exec(ctx, `INSERT INTO captain_sessions (id, source, parent_session_id, root_session_id, parent_relation)
			VALUES ($1, 'claude', $2, $2, CASE WHEN $2::uuid IS NULL THEN NULL ELSE 'transcript' END)`, id, parent)
		return id
	}
	insertRun := func(ctx context.Context, sessionID, rootID uuid.UUID) uuid.UUID {
		GinkgoHelper()
		id := uuid.New()
		exec(ctx, `INSERT INTO captain_prompt_runs (id, session_id, root_session_id) VALUES ($1, $2, $3)`,
			id, sessionID, rootID)
		return id
	}
	sessionChange := func(op RowChangeOp, id uuid.UUID, root uuid.UUID) RowChange {
		return RowChange{Table: RowChangeSessions, Op: op, ID: id, SessionID: &id, RootSessionID: &root}
	}

	It("notifies a root session insert, update and delete with the session as its own root", func(ctx SpecContext) {
		id := insertSession(ctx, nil)
		Expect(drain()).To(Equal([]RowChange{sessionChange(RowChangeInsert, id, id)}))

		exec(ctx, `UPDATE captain_sessions SET title = 'renamed' WHERE id = $1`, id)
		Expect(drain()).To(Equal([]RowChange{sessionChange(RowChangeUpdate, id, id)}))

		exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, id)
		Expect(drain()).To(Equal([]RowChange{sessionChange(RowChangeDelete, id, id)}))
	})

	It("carries a child session's family root", func(ctx SpecContext) {
		root := insertSession(ctx, nil)
		drain()
		child := insertSession(ctx, &root)
		Expect(drain()).To(Equal([]RowChange{sessionChange(RowChangeInsert, child, root)}))
	})

	It("identifies a prompt run by its session, root and itself", func(ctx SpecContext) {
		root := insertSession(ctx, nil)
		drain()
		run := insertRun(ctx, root, root)
		Expect(drain()).To(Equal([]RowChange{{
			Table: RowChangePromptRuns, Op: RowChangeInsert, ID: run,
			SessionID: &root, RootSessionID: &root, PromptRunID: &run,
		}}))
	})

	// An iteration insert advances its run's current_iteration (51), and that
	// cascaded run update is notified too.
	It("identifies an iteration and a turn request by their prompt run", func(ctx SpecContext) {
		root := insertSession(ctx, nil)
		run := insertRun(ctx, root, root)
		drain()

		iteration, request := uuid.New(), uuid.New()
		exec(ctx, `INSERT INTO captain_prompt_run_iterations (id, prompt_run_id, iteration) VALUES ($1, $2, 1)`,
			iteration, run)
		Expect(drain()).To(ConsistOf(
			RowChange{Table: RowChangePromptRunIterations, Op: RowChangeInsert, ID: iteration, PromptRunID: &run},
			RowChange{Table: RowChangePromptRuns, Op: RowChangeUpdate, ID: run, SessionID: &root, RootSessionID: &root, PromptRunID: &run},
		))

		exec(ctx, `INSERT INTO captain_turn_requests (id, session_id, prompt_run_id, kind) VALUES ($1, $2, $3, 'question')`,
			request, root, run)
		Expect(drain()).To(Equal([]RowChange{
			{Table: RowChangeTurnRequests, Op: RowChangeInsert, ID: request, SessionID: &root, PromptRunID: &run},
		}))
	})

	It("notifies both identities when an update re-points a turn request to another run", func(ctx SpecContext) {
		root := insertSession(ctx, nil)
		first := insertRun(ctx, root, root)
		exec(ctx, `UPDATE captain_prompt_runs SET state = 'succeeded', phase = 'finished' WHERE id = $1`, first)
		second := insertRun(ctx, root, root)
		request := uuid.New()
		exec(ctx, `INSERT INTO captain_turn_requests (id, session_id, prompt_run_id, kind) VALUES ($1, $2, $3, 'question')`,
			request, root, first)
		drain()

		exec(ctx, `UPDATE captain_turn_requests SET prompt_run_id = $2 WHERE id = $1`, request, second)
		Expect(drain()).To(ConsistOf(
			RowChange{Table: RowChangeTurnRequests, Op: RowChangeUpdate, ID: request, SessionID: &root, PromptRunID: &first},
			RowChange{Table: RowChangeTurnRequests, Op: RowChangeUpdate, ID: request, SessionID: &root, PromptRunID: &second},
		))
	})

	It("notifies every row a session delete cascades to", func(ctx SpecContext) {
		root := insertSession(ctx, nil)
		run := insertRun(ctx, root, root)
		iteration, request := uuid.New(), uuid.New()
		exec(ctx, `INSERT INTO captain_prompt_run_iterations (id, prompt_run_id, iteration) VALUES ($1, $2, 1)`,
			iteration, run)
		exec(ctx, `INSERT INTO captain_turn_requests (id, session_id, prompt_run_id, kind) VALUES ($1, $2, $3, 'question')`,
			request, root, run)
		drain()

		exec(ctx, `DELETE FROM captain_sessions WHERE id = $1`, root)
		Expect(drain()).To(ConsistOf(
			sessionChange(RowChangeDelete, root, root),
			RowChange{Table: RowChangePromptRuns, Op: RowChangeDelete, ID: run, SessionID: &root, RootSessionID: &root, PromptRunID: &run},
			RowChange{Table: RowChangePromptRunIterations, Op: RowChangeDelete, ID: iteration, PromptRunID: &run},
			RowChange{Table: RowChangeTurnRequests, Op: RowChangeDelete, ID: request, SessionID: &root, PromptRunID: &run},
		))
	})

	It("folds repeated updates of one row in a transaction and drops a rolled-back one", func(ctx SpecContext) {
		root := insertSession(ctx, nil)
		run := insertRun(ctx, root, root)
		drain()

		tx, err := raw.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.ExecContext(ctx, `UPDATE captain_prompt_runs SET current_iteration = $2 WHERE id = $1`, run, 7)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Rollback()).To(Succeed())
		Expect(drain()).To(BeEmpty())

		tx, err = raw.BeginTx(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		for iteration := 1; iteration <= 50; iteration++ {
			_, err = tx.ExecContext(ctx, `UPDATE captain_prompt_runs SET current_iteration = $2 WHERE id = $1`, run, iteration)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(tx.Commit()).To(Succeed())
		Expect(drain()).To(Equal([]RowChange{{
			Table: RowChangePromptRuns, Op: RowChangeUpdate, ID: run,
			SessionID: &root, RootSessionID: &root, PromptRunID: &run,
		}}))
	})

	It("stops with the OnChange error", func(ctx SpecContext) {
		refused := errors.New("projection refused the change")
		listenCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		ready, result := make(chan struct{}, 1), make(chan error, 1)
		go func() {
			result <- db.ListenRowChanges(listenCtx, RowChangeListener{
				OnChange: func(context.Context, RowChange) error { return refused },
				OnResync: func(context.Context) error { ready <- struct{}{}; return nil },
			})
		}()
		Eventually(ready, 10*time.Second).Should(Receive())
		insertSession(ctx, nil)
		Eventually(result, 10*time.Second).Should(Receive(MatchError(refused)))
		drain()
	})
})

var _ = Describe("RowChange decoding", func() {
	id := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	run := uuid.MustParse("00000000-0000-0000-0000-0000000000b2")

	It("decodes the documented payload", func() {
		change, err := decodeRowChange(`{"table":"captain_prompt_run_iterations","op":"DELETE","id":"` +
			id.String() + `","promptRunId":"` + run.String() + `"}`)
		Expect(err).NotTo(HaveOccurred())
		Expect(change).To(Equal(RowChange{Table: RowChangePromptRunIterations, Op: RowChangeDelete, ID: id, PromptRunID: &run}))
	})

	DescribeTable("rejects a payload outside the contract",
		func(payload, message string) {
			_, err := decodeRowChange(payload)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("not JSON", `not-json`, "decode captain_row_change payload"),
		Entry("unknown table", `{"table":"captain_messages","op":"INSERT","id":"`+id.String()+`"}`, `unknown table "captain_messages"`),
		Entry("unknown op", `{"table":"captain_sessions","op":"TRUNCATE","id":"`+id.String()+`"}`, `unknown op "TRUNCATE"`),
		Entry("missing id", `{"table":"captain_sessions","op":"INSERT"}`, "has no id"),
	)
})

var _ = Describe("ListenRowChanges", func() {
	It("requires both callbacks", func(ctx SpecContext) {
		Expect((&DB{}).ListenRowChanges(ctx, RowChangeListener{})).To(MatchError(ContainSubstring("OnChange is required")))
		Expect((&DB{}).ListenRowChanges(ctx, RowChangeListener{
			OnChange: func(context.Context, RowChange) error { return nil },
		})).To(MatchError(ContainSubstring("OnResync is required")))
	})
})
