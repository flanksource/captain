package database

import (
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("nested agent roots", Ordered, func() {
	var db *DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_nested_agent_roots"})
		var err error
		db, err = Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	// grandparent ← parent ← child, with the parent's own hierarchy unknown
	// until it is adopted.
	nest := func(ctx SpecContext, name string) (grandparent, parent *Session) {
		GinkgoHelper()
		var err error
		grandparent, err = db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: name + "-grandparent", Source: "codex", HostID: "nested-test",
		})
		Expect(err).NotTo(HaveOccurred())
		parent, err = db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: name + "-parent", Source: "codex", HostID: "nested-test",
		})
		Expect(err).NotTo(HaveOccurred())
		return grandparent, parent
	}

	It("re-derives a child's root that went stale when its parent was adopted", func(ctx SpecContext) {
		grandparent, parent := nest(ctx, "stale")
		child := CreateSessionInput{
			ProviderSessionID: "stale-child", Source: "codex", HostID: "nested-test", ParentSessionID: &parent.ID,
		}
		_, err := db.CreateOrGetSession(ctx, child)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: "stale-parent", Source: "codex", HostID: "nested-test", ParentSessionID: &grandparent.ID,
		})
		Expect(err).NotTo(HaveOccurred())
		// The row a child insert racing the adoption leaves behind.
		Expect(db.Gorm().Exec(`UPDATE captain_sessions SET root_session_id = ? WHERE provider_session_id = 'stale-child'`,
			parent.ID).Error).To(Succeed())

		reingested, err := db.CreateOrGetSession(ctx, child)
		Expect(err).NotTo(HaveOccurred())
		Expect(reingested.ParentSessionID).To(Equal(&parent.ID))
		Expect(reingested.RootSessionID).To(Equal(&grandparent.ID))
	})

	It("derives a child's root after a concurrent adoption of its parent commits", func(ctx SpecContext) {
		grandparent, parent := nest(ctx, "racing")
		adoption := db.Gorm().Begin()
		DeferCleanup(func() { adoption.Rollback() })
		Expect(adoption.Exec(`UPDATE captain_sessions SET parent_session_id = ?, parent_relation = 'agent', root_session_id = ? WHERE id = ?`,
			grandparent.ID, grandparent.ID, parent.ID).Error).To(Succeed())

		created := make(chan *Session, 1)
		go func() {
			defer GinkgoRecover()
			child, err := db.CreateOrGetSession(ctx, CreateSessionInput{
				ProviderSessionID: "racing-child", Source: "codex", HostID: "nested-test", ParentSessionID: &parent.ID,
			})
			Expect(err).NotTo(HaveOccurred())
			created <- child
		}()
		Consistently(created, "300ms").ShouldNot(Receive(), "the child insert must wait for the parent's adoption")
		Expect(adoption.Commit().Error).To(Succeed())

		var child *Session
		Eventually(created, "5s").Should(Receive(&child))
		Expect(child.RootSessionID).To(Equal(&grandparent.ID))
	})
})

var _ = Describe("session hierarchy enrichment", func() {
	It("records transcript ownership when an existing provider session is adopted", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_transcript_hierarchy"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		providerSessionID := "provider-session-before-chat"
		providerSession, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID,
			Source:            "claude",
			Provider:          "anthropic",
			HostID:            "hierarchy-test",
		})
		Expect(err).NotTo(HaveOccurred())

		chat, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			Source: "aichat", Provider: "captain", HostID: "hierarchy-test",
		})
		Expect(err).NotTo(HaveOccurred())

		adopted, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID,
			Source:            "claude",
			Provider:          "anthropic",
			HostID:            "hierarchy-test",
			ParentSessionID:   &chat.ID,
			ParentRelation:    SessionParentRelationTranscript,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(adopted.ID).To(Equal(providerSession.ID))
		Expect(adopted.ParentSessionID).To(Equal(&chat.ID))
		Expect(adopted.RootSessionID).To(Equal(&chat.ID))
		Expect(adopted.ParentRelation).To(Equal(SessionParentRelationTranscript))

		_, err = db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID,
			Source:            "claude",
			Provider:          "anthropic",
			HostID:            "hierarchy-test",
			ParentSessionID:   &chat.ID,
			ParentRelation:    SessionParentRelationAgent,
		})
		Expect(err).To(MatchError(ContainSubstring("different hierarchy")))
	})

	It("adopts a monitored provider session into its owning operation tree", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_hierarchy"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		todoID := uuid.New()
		root, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: todoID, Source: "gavel", Provider: "todos", HostID: "hierarchy-test",
			AgentType: "todo",
			Metadata: map[string]any{
				"tags":  []string{"todo"},
				"links": map[string]string{"todo": todoID.String()},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(root.ID).To(Equal(todoID))
		Expect(root.Metadata).To(HaveKeyWithValue("links", HaveKeyWithValue("todo", todoID.String())))

		providerSessionID := "provider-session-before-admission"
		admissionID := uuid.New()
		admission, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: admissionID, ProviderSessionID: providerSessionID,
			Source: "gavel", Provider: "codex", HostID: "hierarchy-test",
			ParentSessionID: &todoID, AgentType: "run",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(admission.RootSessionID).NotTo(BeNil())
		Expect(*admission.RootSessionID).To(Equal(todoID))

		observed, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID,
			Source:            "codex",
			Provider:          "openai",
			HostID:            "hierarchy-test",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(observed.ParentSessionID).To(BeNil())
		observedChildID := uuid.New()
		observedChild, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: observedChildID, Source: "codex", Provider: "openai", HostID: "hierarchy-test",
			ParentSessionID: &observed.ID,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(observedChild.RootSessionID).To(Equal(&observed.ID))

		linked, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID,
			Source:            "codex",
			Provider:          "openai",
			HostID:            "hierarchy-test",
			ParentSessionID:   &admissionID,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(linked.ID).To(Equal(observed.ID))
		Expect(linked.ParentSessionID).NotTo(BeNil())
		Expect(*linked.ParentSessionID).To(Equal(admissionID))
		Expect(linked.RootSessionID).NotTo(BeNil())
		Expect(*linked.RootSessionID).To(Equal(todoID))
		observedChild, err = db.GetSession(ctx, observedChildID)
		Expect(err).NotTo(HaveOccurred())
		Expect(observedChild.RootSessionID).To(Equal(&todoID))

		replayed, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID,
			Source:            "codex",
			Provider:          "openai",
			HostID:            "hierarchy-test",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed.ParentSessionID).To(Equal(linked.ParentSessionID))
		Expect(replayed.RootSessionID).To(Equal(linked.RootSessionID))

		otherAdmission, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: uuid.New(), Source: "gavel", Provider: "codex", HostID: "hierarchy-test",
			ParentSessionID: &todoID, AgentType: "run",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID,
			Source:            "codex",
			Provider:          "openai",
			HostID:            "hierarchy-test",
			ParentSessionID:   &otherAdmission.ID,
		})
		Expect(err).To(MatchError(ContainSubstring("existing session has a different hierarchy")))
		Expect(err).To(MatchError(MatchRegexp("session conflict")))

		run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{
			SessionID: admission.ID, ExecutionSessionID: &linked.ID,
			AdmissionKey: "gavel-todo-session-hierarchy",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(run.RootSessionID).To(Equal(todoID))
		Expect(run.ExecutionSessionID).To(Equal(&linked.ID))
	})
})
