package database

import (
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// openTranscriptSourceDB is one database per spec file; the specs below key
// every row on their own provider identity so they can share it.
func openTranscriptSourceDB(ctx SpecContext) *DB {
	GinkgoHelper()
	handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_transcript_sources"})
	db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	return db
}

// gavelRun builds the two rows a Gavel TODO run actually produces: the
// admission root it books the run against (source `gavel`, no transcript), and
// the provider row the agent's on-disk log is ingested into.
func gavelRun(ctx SpecContext, db *DB, providerSessionID string) (root, transcript *Session) {
	GinkgoHelper()
	root, err := db.CreateOrGetSession(ctx, CreateSessionInput{
		ProviderSessionID: providerSessionID, Source: "gavel", Provider: "claude",
		HostID: "transcript-test", CWD: "/Users/dev/go/src/example",
	})
	Expect(err).NotTo(HaveOccurred())
	transcript, err = db.CreateOrGetSession(ctx, CreateSessionInput{
		ProviderSessionID: providerSessionID, Source: "claude", Provider: "anthropic",
		HostID: "transcript-test", ParentSessionID: &root.ID, ParentRelation: SessionParentRelationTranscript,
		Path: "/Users/dev/.claude/projects/worktree/" + providerSessionID + ".jsonl",
	})
	Expect(err).NotTo(HaveOccurred())
	return root, transcript
}

var _ = Describe("transcript source registration", func() {
	It("binds a transcript file to a session by id", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		_, transcript := gavelRun(ctx, db, "0199d0aa-0000-7000-8000-000000000001")
		path := "/Users/dev/.claude/projects/-worktree-a/registered.jsonl"

		Expect(db.RegisterTranscriptSource(ctx, transcript.ID, "claude", path, transcript.ProviderSessionID)).To(Succeed())

		sources, err := db.ListSessionSources(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(sources).To(HaveKey(path))
		state := sources[path]
		Expect(state.SessionID).To(Equal(transcript.ID))
		Expect(state.SourceKind).To(Equal("claude"))
		Expect(state.SourceIdentity).To(Equal(transcript.ProviderSessionID))
		// A registration is a claim on work still to do. If it recorded the
		// ingestor's parser version and an observed mtime, needsIngest would skip
		// the file forever and the registration would suppress the very ingest it
		// exists to trigger.
		Expect(state.ObservedModTime).To(BeNil())
		Expect(state.ObservedSize).To(BeZero())
		Expect(state.ByteOffset).To(BeZero())
	})

	It("leaves ingest bookkeeping untouched when a registered transcript is re-registered", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		_, transcript := gavelRun(ctx, db, "0199d0aa-0000-7000-8000-000000000002")
		path := "/Users/dev/.claude/projects/-worktree-b/ingested.jsonl"
		Expect(db.RegisterSessionSource(ctx, transcript.ID, IngestSourceInput{
			SourceKind: "claude", Path: path, SourceIdentity: transcript.ProviderSessionID,
			ParserVersion: 5, ByteOffset: 4096, ObservedSize: 4096, LastEventKey: "128",
		})).To(Succeed())

		Expect(db.RegisterTranscriptSource(ctx, transcript.ID, "claude", path, transcript.ProviderSessionID)).To(Succeed())

		sources, err := db.ListSessionSources(ctx)
		Expect(err).NotTo(HaveOccurred())
		state := sources[path]
		Expect(state.ParserVersion).To(Equal(5))
		Expect(state.ByteOffset).To(BeEquivalentTo(4096), "re-registering must not rewind the ingest cursor")
		Expect(state.LastEventKey).To(Equal("128"))
	})

	It("refuses a registration that names no session, source or path", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		_, transcript := gavelRun(ctx, db, "0199d0aa-0000-7000-8000-000000000003")
		Expect(db.RegisterTranscriptSource(ctx, uuid.Nil, "claude", "/tmp/a.jsonl", "x")).To(MatchError(ErrInvalidIngest))
		Expect(db.RegisterTranscriptSource(ctx, transcript.ID, "", "/tmp/a.jsonl", "x")).To(MatchError(ErrInvalidIngest))
		Expect(db.RegisterTranscriptSource(ctx, transcript.ID, "claude", "  ", "x")).To(MatchError(ErrInvalidIngest))
	})

	It("refuses to bind a transcript to a session that does not exist", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		Expect(db.RegisterTranscriptSource(ctx, uuid.New(), "claude", "/tmp/orphan.jsonl", "x")).
			To(MatchError(ErrSessionNotFound))
	})
})

var _ = Describe("transcript session resolution", func() {
	It("resolves a Gavel-created run's transcript child from its admission root", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		root, transcript := gavelRun(ctx, db, "0199d0bb-0000-7000-8000-000000000001")

		found, err := db.GetTranscriptSession(ctx, root.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found.ID).To(Equal(transcript.ID))
		Expect(found.ParentRelation).To(Equal(SessionParentRelationTranscript))
	})

	It("hops from the admission root to the transcript-bearing sibling that shares its provider id", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		providerSessionID := "0199d0bb-0000-7000-8000-000000000002"
		root, transcript := gavelRun(ctx, db, providerSessionID)

		resolved, err := db.GetTranscriptSessionByIdentity(ctx, providerSessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.ID).To(Equal(transcript.ID))
		Expect(resolved.ID).NotTo(Equal(root.ID), "the admission root holds no transcript")
	})

	It("reports a provider id with no provider session at all as not found", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		providerSessionID := "0199d0bb-0000-7000-8000-000000000003"
		_, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: "gavel", Provider: "claude", HostID: "transcript-test",
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = db.GetTranscriptSessionByIdentity(ctx, providerSessionID)
		Expect(err).To(MatchError(ErrSessionNotFound))
	})

	It("refuses to guess when a provider id has several transcript-bearing sessions", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		providerSessionID := "0199d0bb-0000-7000-8000-000000000004"
		gavelRun(ctx, db, providerSessionID)
		_, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ProviderSessionID: providerSessionID, Source: "codex", Provider: "openai",
			HostID: "transcript-test", Path: "/Users/dev/.codex/sessions/rollout-" + providerSessionID + ".jsonl",
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = db.GetTranscriptSessionByIdentity(ctx, providerSessionID)
		Expect(err).To(MatchError(ErrSessionConflict))
	})

	It("requires a provider session id", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		_, err := db.GetTranscriptSessionByIdentity(ctx, "  ")
		Expect(err).To(MatchError(ErrInvalidSession))
	})
})

var _ = Describe("session working directory", func() {
	// The worktree an agent runs in is created per run and recorded nowhere: the
	// admission root carries the repository root, and transcript ingest only
	// learns a cwd once a transcript exists. A run that blocks before its first
	// turn boundary therefore has no cwd at all.
	It("records the worktree the agent actually ran in", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		root, _ := gavelRun(ctx, db, "0199d0cc-0000-7000-8000-000000000001")
		Expect(root.CWD).To(Equal("/Users/dev/go/src/example"))
		worktree := "/Users/dev/go/src/example/.shell/worktrees/shell-9f3a-0198"

		updated, err := db.UpdateSessionState(ctx, UpdateSessionStateInput{
			ID: root.ID, ExpectedVersion: root.StateVersion, CWD: &worktree,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.CWD).To(Equal(worktree))

		reread, err := db.GetSession(ctx, root.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(reread.CWD).To(Equal(worktree))
	})

	It("stores one spelling of the working directory, exactly as ingest does", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		root, _ := gavelRun(ctx, db, "0199d0cc-0000-7000-8000-000000000002")
		worktree := "/Users/dev/go/src/example/.shell/worktrees/shell-aaaa-0199//"

		updated, err := db.UpdateSessionState(ctx, UpdateSessionStateInput{
			ID: root.ID, ExpectedVersion: root.StateVersion, CWD: &worktree,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.CWD).To(Equal("/Users/dev/go/src/example/.shell/worktrees/shell-aaaa-0199"))
	})

	It("refuses to clear the working directory", func(ctx SpecContext) {
		db := openTranscriptSourceDB(ctx)
		root, _ := gavelRun(ctx, db, "0199d0cc-0000-7000-8000-000000000003")
		blank := "   "
		_, err := db.UpdateSessionState(ctx, UpdateSessionStateInput{
			ID: root.ID, ExpectedVersion: root.StateVersion, CWD: &blank,
		})
		Expect(err).To(MatchError(ErrInvalidSession))
	})
})
