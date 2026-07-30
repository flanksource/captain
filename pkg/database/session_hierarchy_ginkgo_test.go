package database

import (
	"os"
	"path/filepath"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("session hierarchy enrichment", func() {
	It("adopts a monitored provider session into its owning operation tree", func(ctx SpecContext) {
		if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
			Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres store tests")
		}

		dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
			DataDir:  filepath.Join(GinkgoT().TempDir(), "postgres"),
			Database: "captain_session_hierarchy",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(stop()).To(Succeed()) })

		db, err := Open(ctx, WithDSN(dsn), WithMigrations())
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

		admissionID := uuid.New()
		admission, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: admissionID, Source: "gavel", Provider: "codex", HostID: "hierarchy-test",
			ParentSessionID: &todoID, AgentType: "run",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(admission.RootSessionID).NotTo(BeNil())
		Expect(*admission.RootSessionID).To(Equal(todoID))

		providerSessionID := "provider-session-before-admission"
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

		run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{
			SessionID: admission.ID, ExecutionSessionID: &linked.ID,
			AdmissionKey: "gavel-todo-session-hierarchy",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(run.RootSessionID).To(Equal(todoID))
		Expect(run.ExecutionSessionID).To(Equal(&linked.ID))
	})
})
