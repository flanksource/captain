package sessiontree_test

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/sessiontree"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	hostSource   = "acme-host"
	hostProvider = "tracker"
	hostID       = "hierarchy-test"
)

var _ = Describe("Ensure", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_hierarchy"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	rootSpec := func(id uuid.UUID, title string, metadata map[string]any) database.CreateSessionInput {
		return database.CreateSessionInput{
			ID: id, Source: hostSource, Provider: hostProvider, HostID: hostID,
			Title: title, AgentType: "item", Metadata: metadata,
		}
	}
	childSpec := func(id, parent uuid.UUID, operation string) database.CreateSessionInput {
		return database.CreateSessionInput{
			ID: id, Source: hostSource, Provider: "executor", HostID: hostID,
			ParentSessionID: &parent, AgentType: operation, Title: "item · " + operation,
		}
	}
	noLink := func(context.Context, *database.DB, []*database.Session) error { return nil }
	expectAbsent := func(ctx context.Context, ids ...uuid.UUID) {
		for _, id := range ids {
			_, err := db.GetSession(ctx, id)
			Expect(err).To(MatchError(database.ErrSessionNotFound), "session %s should not exist", id)
		}
	}

	It("creates an ordered tree whose child resolves its parent and aggregate root from the batch", func(ctx SpecContext) {
		rootID, childID := uuid.New(), uuid.New()

		sessions, err := sessiontree.Ensure(ctx, db, []database.CreateSessionInput{
			rootSpec(rootID, "item", nil), childSpec(childID, rootID, "plan"),
		}, noLink)

		Expect(err).NotTo(HaveOccurred())
		Expect(sessions).To(HaveLen(2))
		Expect(sessions[0].ID).To(Equal(rootID))
		Expect(sessions[0].ParentSessionID).To(BeNil())
		Expect(sessions[0].RootSessionID).To(BeNil())
		Expect(sessions[1].ID).To(Equal(childID))
		Expect(sessions[1].ParentSessionID).To(Equal(&rootID))
		Expect(sessions[1].RootSessionID).To(Equal(&rootID))
		Expect(sessions[1].ParentRelation).To(Equal(database.SessionParentRelationAgent))
	})

	It("is idempotent on caller-supplied IDs", func(ctx SpecContext) {
		rootID, childID := uuid.New(), uuid.New()
		specs := []database.CreateSessionInput{rootSpec(rootID, "item", nil), childSpec(childID, rootID, "run")}
		first, err := sessiontree.Ensure(ctx, db, specs, noLink)
		Expect(err).NotTo(HaveOccurred())

		second, err := sessiontree.Ensure(ctx, db, specs, noLink)

		Expect(err).NotTo(HaveOccurred())
		Expect([]uuid.UUID{second[0].ID, second[1].ID}).To(Equal([]uuid.UUID{first[0].ID, first[1].ID}))
		Expect(second[1].CreatedAt).To(Equal(first[1].CreatedAt))
	})

	It("attaches a child to a parent that already exists outside the batch", func(ctx SpecContext) {
		rootID, childID := uuid.New(), uuid.New()
		_, err := sessiontree.Ensure(ctx, db, []database.CreateSessionInput{rootSpec(rootID, "item", nil)}, noLink)
		Expect(err).NotTo(HaveOccurred())

		sessions, err := sessiontree.Ensure(ctx, db, []database.CreateSessionInput{childSpec(childID, rootID, "verify")}, noLink)

		Expect(err).NotTo(HaveOccurred())
		Expect(sessions[0].RootSessionID).To(Equal(&rootID))
	})

	It("runs link in the same transaction with the sessions in spec order", func(ctx SpecContext) {
		rootID, childID := uuid.New(), uuid.New()
		var linked []uuid.UUID

		_, err := sessiontree.Ensure(ctx, db, []database.CreateSessionInput{
			rootSpec(rootID, "item", nil), childSpec(childID, rootID, "run"),
		}, func(ctx context.Context, tx *database.DB, sessions []*database.Session) error {
			for _, session := range sessions {
				linked = append(linked, session.ID)
			}
			_, err := tx.GetSession(ctx, childID)
			return err
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(linked).To(Equal([]uuid.UUID{rootID, childID}))
	})

	It("rolls the whole tree back when link fails", func(ctx SpecContext) {
		rootID, childID := uuid.New(), uuid.New()
		linkErr := errors.New("host row rejected")

		_, err := sessiontree.Ensure(ctx, db, []database.CreateSessionInput{
			rootSpec(rootID, "item", nil), childSpec(childID, rootID, "run"),
		}, func(context.Context, *database.DB, []*database.Session) error { return linkErr })

		Expect(err).To(MatchError(linkErr))
		expectAbsent(ctx, rootID, childID)
	})

	It("rolls back sessions ensured with EnsureTx when the enclosing transaction fails", func(ctx SpecContext) {
		rootID, childID := uuid.New(), uuid.New()
		hostErr := errors.New("host admission failed")

		err := db.Transaction(ctx, func(tx *database.DB) error {
			_, err := sessiontree.EnsureTx(ctx, tx, []database.CreateSessionInput{
				rootSpec(rootID, "item", nil), childSpec(childID, rootID, "run"),
			})
			Expect(err).NotTo(HaveOccurred())
			return hostErr
		})

		Expect(err).To(MatchError(hostErr))
		expectAbsent(ctx, rootID, childID)
	})

	It("merges metadata top-level on an existing session and leaves its descriptive fields unchanged", func(ctx SpecContext) {
		rootID := uuid.New()
		_, err := sessiontree.Ensure(ctx, db, []database.CreateSessionInput{rootSpec(rootID, "original title", map[string]any{
			"tags":  []any{"item"},
			"links": map[string]any{"item": rootID.String(), "stale": "x"},
		})}, noLink)
		Expect(err).NotTo(HaveOccurred())

		sessions, err := sessiontree.Ensure(ctx, db, []database.CreateSessionInput{rootSpec(rootID, "renamed title", map[string]any{
			"links": map[string]any{"item": rootID.String(), "plan": "p1"},
		})}, noLink)

		Expect(err).NotTo(HaveOccurred())
		Expect(sessions[0].Title).To(Equal("original title"))
		Expect(sessions[0].Metadata).To(Equal(map[string]any{
			"tags":  []any{"item"},
			"links": map[string]any{"item": rootID.String(), "plan": "p1"},
		}))
	})

	DescribeTable("rejects an invalid tree before writing anything",
		func(ctx SpecContext, build func(rootID uuid.UUID) []database.CreateSessionInput, link sessiontree.Link, want error) {
			rootID := uuid.New()

			_, err := sessiontree.Ensure(ctx, db, build(rootID), link)

			Expect(err).To(MatchError(want))
			expectAbsent(ctx, rootID)
		},
		Entry("an empty batch", func(uuid.UUID) []database.CreateSessionInput { return nil },
			sessiontree.Link(noLink), database.ErrInvalidSession),
		Entry("a nil link", func(rootID uuid.UUID) []database.CreateSessionInput {
			return []database.CreateSessionInput{rootSpec(rootID, "item", nil)}
		}, sessiontree.Link(nil), database.ErrInvalidSession),
		Entry("a caller-supplied root session ID", func(rootID uuid.UUID) []database.CreateSessionInput {
			other := uuid.New()
			spec := rootSpec(rootID, "item", nil)
			spec.RootSessionID = &other
			return []database.CreateSessionInput{spec}
		}, sessiontree.Link(noLink), database.ErrInvalidSession),
		Entry("a duplicate ID", func(rootID uuid.UUID) []database.CreateSessionInput {
			return []database.CreateSessionInput{rootSpec(rootID, "item", nil), rootSpec(rootID, "item", nil)}
		}, sessiontree.Link(noLink), database.ErrInvalidSession),
		Entry("a child listed before its in-batch parent", func(rootID uuid.UUID) []database.CreateSessionInput {
			return []database.CreateSessionInput{childSpec(uuid.New(), rootID, "run"), rootSpec(rootID, "item", nil)}
		}, sessiontree.Link(noLink), database.ErrInvalidSession),
		Entry("a parent neither in the batch nor stored", func(rootID uuid.UUID) []database.CreateSessionInput {
			return []database.CreateSessionInput{rootSpec(rootID, "item", nil), childSpec(uuid.New(), uuid.New(), "run")}
		}, sessiontree.Link(noLink), database.ErrSessionNotFound),
	)

	It("rejects a root spec that resolves to a stored child session", func(ctx SpecContext) {
		rootID, childID := uuid.New(), uuid.New()
		_, err := sessiontree.Ensure(ctx, db, []database.CreateSessionInput{
			rootSpec(rootID, "item", nil), childSpec(childID, rootID, "run"),
		}, noLink)
		Expect(err).NotTo(HaveOccurred())

		_, err = sessiontree.Ensure(ctx, db, []database.CreateSessionInput{rootSpec(childID, "item", nil)}, noLink)

		Expect(err).To(MatchError(database.ErrSessionConflict))
	})

	It("refuses EnsureTx on a handle that is not inside a transaction", func(ctx SpecContext) {
		rootID := uuid.New()

		_, err := sessiontree.EnsureTx(ctx, db, []database.CreateSessionInput{rootSpec(rootID, "item", nil)})

		Expect(err).To(MatchError(sessiontree.ErrNoTransaction))
		expectAbsent(ctx, rootID)
	})
})
