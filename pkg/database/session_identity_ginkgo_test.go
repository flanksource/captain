// Tests what makes a written session the same as one already recorded: a caller-supplied ID
// names one session from any host, while a provider session is its source, host and provider ID.
package database

import (
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("a session's identity", Ordered, func() {
	var db *DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_identity"})
		var err error
		db, err = Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	// gavel names a todo's session by the todo's ID from whichever host serves it: a restarted
	// Kubernetes pod has a new hostname, and the todo must still run again.
	It("is the caller's ID, whichever host writes it", func(ctx SpecContext) {
		todoID := uuid.New()
		first, err := db.CreateOrGetSession(ctx, CreateSessionInput{ID: todoID, Source: "gavel", Provider: "todos", HostID: "gavel-pod-a"})
		Expect(err).NotTo(HaveOccurred())

		again, err := db.CreateOrGetSession(ctx, CreateSessionInput{ID: todoID, Source: "gavel", Provider: "todos", HostID: "gavel-pod-b"})

		Expect(err).NotTo(HaveOccurred())
		Expect(again.ID).To(Equal(first.ID))
		Expect(again.HostID).To(Equal("gavel-pod-a"), "a session keeps the host that first recorded it")
	})

	It("refuses the caller's ID from another source", func(ctx SpecContext) {
		id := uuid.New()
		_, err := db.CreateOrGetSession(ctx, CreateSessionInput{ID: id, Source: "gavel", Provider: "todos", HostID: "gavel-pod-a"})
		Expect(err).NotTo(HaveOccurred())

		_, err = db.CreateOrGetSession(ctx, CreateSessionInput{ID: id, Source: "claude", Provider: "anthropic", HostID: "gavel-pod-a"})

		Expect(err).To(MatchError(ErrSessionConflict))
		Expect(err).To(MatchError(ContainSubstring("different provider identity")))
	})

	It("is a provider session's host as well as its provider ID", func(ctx SpecContext) {
		here, err := db.CreateOrGetSession(ctx, CreateSessionInput{ProviderSessionID: "thread-identity", Source: "claude", Provider: "anthropic", HostID: "laptop"})
		Expect(err).NotTo(HaveOccurred())

		elsewhere, err := db.CreateOrGetSession(ctx, CreateSessionInput{ProviderSessionID: "thread-identity", Source: "claude", Provider: "anthropic", HostID: "workstation"})

		Expect(err).NotTo(HaveOccurred())
		Expect(elsewhere.ID).NotTo(Equal(here.ID), "the same provider ID on another host is another transcript")
	})
})
