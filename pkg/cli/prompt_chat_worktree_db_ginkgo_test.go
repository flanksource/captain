package cli

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/shell"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("resuming a session in its recorded worktree", func() {
	var (
		db                   *database.DB
		admission, execution *database.Session
		state                api.WorktreeState
	)

	BeforeEach(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_resume_worktree"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		setCaptainDBForTest(db)
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})
		state = removedWorktree()
		// A gavel run as captain stores it: the launcher's row, and the claude row
		// its transcript was ingested into.
		providerID := uuid.NewString()
		admission, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "gavel", Provider: "agent-claude", HostID: captainHostID(), CWD: state.Repo,
		})
		Expect(err).NotTo(HaveOccurred())
		execution, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "claude", Provider: "anthropic", HostID: captainHostID(), CWD: state.Path,
			ParentSessionID: &admission.ID, ParentRelation: database.SessionParentRelationTranscript,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	// recordRun files a finished run of the session with the workspace it left.
	recordRun := func(ctx context.Context, sessionID uuid.UUID, workspace *api.WorkspaceRecord) {
		GinkgoHelper()
		run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{SessionID: sessionID, ExecutionSessionID: &execution.ID})
		Expect(err).NotTo(HaveOccurred())
		succeeded, finished := database.PromptRunStateSucceeded, database.PromptRunPhaseFinished
		_, err = db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
			ID: run.ID, ExpectedVersion: run.Version, State: &succeeded, Phase: &finished, Workspace: workspace,
		})
		Expect(err).NotTo(HaveOccurred())
	}

	workspaceOf := func(worktree api.WorktreeState) *api.WorkspaceRecord {
		return &api.WorkspaceRecord{Cwd: worktree.Path, Worktree: &worktree}
	}

	resume := func(ctx context.Context) (PromptRenderResult, error) {
		GinkgoHelper()
		item, err := resolveSessionItem(ctx, admission.ID.String())
		Expect(err).NotTo(HaveOccurred())
		return resumeRenderResult(ctx, item, ChatMessageRequest{Text: "continue", Model: "claude-opus-5-5"})
	}

	expectRecreates := func(rendered PromptRenderResult, want api.WorktreeState) {
		GinkgoHelper()
		Expect(rendered.Input.SessionID).To(Equal(admission.ProviderSessionID))
		Expect(rendered.Input.Setup.Cwd).To(Equal(want.Repo))
		Expect(rendered.Input.Setup.Checkout.Worktree).To(Equal(&shell.Worktree{
			Mode: shell.WorktreeBranch, Branch: want.Branch, Path: want.Path, Keep: true,
			Uncommitted: shell.CloneSkip, Ignored: shell.CloneClone,
		}))
	}

	It("recreates the worktree the launcher's run recorded", func(ctx SpecContext) {
		recordRun(ctx, admission.ID, workspaceOf(state))

		rendered, err := resume(ctx)

		Expect(err).NotTo(HaveOccurred())
		expectRecreates(rendered, state)
	})

	It("finds a run recorded on the transcript's own session", func(ctx SpecContext) {
		recordRun(ctx, execution.ID, workspaceOf(state))

		rendered, err := resume(ctx)

		Expect(err).NotTo(HaveOccurred())
		expectRecreates(rendered, state)
	})

	It("looks past a later turn that recorded no workspace", func(ctx SpecContext) {
		recordRun(ctx, admission.ID, workspaceOf(state))
		recordRun(ctx, admission.ID, nil)

		rendered, err := resume(ctx)

		Expect(err).NotTo(HaveOccurred())
		expectRecreates(rendered, state)
	})

	It("uses the newest record of the directory, across both sessions", func(ctx SpecContext) {
		stale := state
		stale.Branch = "shell/stale"
		recordRun(ctx, execution.ID, workspaceOf(stale))
		recordRun(ctx, admission.ID, workspaceOf(state))

		rendered, err := resume(ctx)

		Expect(err).NotTo(HaveOccurred())
		expectRecreates(rendered, state)
	})

	DescribeTable("reports the directory unavailable when no run recorded a worktree there",
		func(ctx SpecContext, record func(context.Context)) {
			record(ctx)

			_, err := resume(ctx)

			Expect(chatErrorStatus(err)).To(Equal(http.StatusUnprocessableEntity))
			Expect(err).To(MatchError("session working directory " + state.Path + " is unavailable"))
		},
		Entry("no run", func(context.Context) {}),
		Entry("a run in the repository itself", func(ctx context.Context) {
			recordRun(ctx, admission.ID, &api.WorkspaceRecord{Cwd: state.Path})
		}),
		Entry("a run in another worktree", func(ctx context.Context) {
			other := state
			other.Path = filepath.Join(filepath.Dir(state.Path), "other")
			recordRun(ctx, admission.ID, workspaceOf(other))
		}),
	)
})
