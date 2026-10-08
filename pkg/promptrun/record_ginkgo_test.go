package promptrun_test

import (
	"context"
	"errors"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
)

// eventsProvider streams one scripted turn per call. block holds the turn open
// until the run's context ends, which is how a stopped run looks to the loop.
type eventsProvider struct {
	mu     sync.Mutex
	calls  int
	events []ai.Event
	block  bool
}

func (p *eventsProvider) GetModel() string { return "claude-sonnet-5" }
func (p *eventsProvider) GetRuntime() api.Runtime {
	return api.RuntimeOf(api.Anthropic, api.ModeAgent)
}
func (p *eventsProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return nil, errors.New("eventsProvider only streams")
}
func (p *eventsProvider) ExecuteStream(ctx context.Context, _ ai.Request) (<-chan ai.Event, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	ch := make(chan ai.Event, len(p.events))
	for _, ev := range p.events {
		ch <- ev
	}
	if !p.block {
		close(ch)
		return ch, nil
	}
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func (p *eventsProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func answeredTurn(providerSessionID string) []ai.Event {
	return []ai.Event{
		{Kind: ai.EventSystem, SessionID: providerSessionID, Model: "claude-sonnet-5"},
		{Kind: ai.EventText, Text: "done", Model: "claude-sonnet-5"},
		{Kind: ai.EventResult, Success: true, Model: "claude-sonnet-5", Usage: &ai.Usage{InputTokens: 3, OutputTokens: 2}},
	}
}

// failingVerifier reports one in-flight snapshot and then fails the round.
type failingVerifier struct{ progress func(api.VerifyReport) }

func (v *failingVerifier) SetProgress(fn func(api.VerifyReport)) { v.progress = fn }
func (v *failingVerifier) Verify(context.Context, string, []string) (verify.Verdict, error) {
	running := api.NewNodeReport(api.VerifyKindFixture, "fixture", api.VerifyNode{Name: "acceptance", Running: true})
	running.Iteration = 1
	v.progress(running)
	final := api.NewNodeReport(api.VerifyKindFixture, "fixture", api.VerifyNode{Name: "acceptance", Passed: false})
	final.Reason = "acceptance criteria unmet"
	return verify.Verdict{OK: false, Report: &final}, nil
}

// workspaceHook stands in for the setup and commit hooks: it records a worktree
// and a commit on the run's workspace, plus the transient detail (a notice and a
// diff) the durable record must leave out.
type workspaceHook struct {
	worktree *api.WorktreeState
	commit   api.CommitRecord
}

func (h *workspaceHook) Name() string { return "workspace-stub" }
func (h *workspaceHook) PreRun(hc *agent.HookContext) error {
	ws := hc.Workspace()
	ws.Worktree = h.worktree
	ws.AddCommit(h.commit.SHA, h.commit.Message)
	ws.Diff = "diff --git a/x b/x"
	return nil
}

func openRecordDB() *database.DB {
	GinkgoHelper()
	handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_promptrun_record"})
	db, err := database.Open(GinkgoT().Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	return db
}

// hostTree is the session pair a host books its runs against: its own root, and
// the operation session the run is admitted on.
func hostTree() []database.CreateSessionInput {
	root := uuid.New()
	return []database.CreateSessionInput{
		{ID: root, Source: "gavel", Provider: "todos", HostID: "record-test", CWD: "/work/repo", Title: "Fix the build"},
		{ID: uuid.New(), Source: "gavel", Provider: "cmux-claude", HostID: "record-test", ParentSessionID: &root,
			CWD: "/work/repo", Title: "Fix the build · implement", Description: "Gavel TODO implement"},
	}
}

var _ = Describe("a recorded prompt run", func() {
	var (
		db       *database.DB
		cwd      string
		resolved api.ResolvedSpec
	)

	BeforeEach(func() {
		db = openRecordDB()
		cwd = GinkgoT().TempDir()
		spec := api.Spec{
			Model:  api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent, Effort: api.EffortHigh},
			Prompt: api.Prompt{User: "fix the build"},
		}
		spec.SetCwd(cwd)
		resolved = api.ResolvedSpec{
			Spec: spec,
			Trace: []api.SpecLayer{
				{Name: ".gavel.yaml ai", Source: api.SpecLayerSourcePrompt, Scope: api.SpecLayerSurface, Spec: api.Spec{Model: api.Model{Name: "claude-sonnet-5"}}},
				api.RequestSpecLayer("request", api.Spec{Prompt: api.Prompt{User: "fix the build"}}),
			},
			Warnings: []string{"effort defaulted to high"},
		}
	})

	AfterEach(func() { verify.Unregister(verify.KindFixture) })

	recording := func(link func(context.Context, *database.DB, *database.PromptRun) error) *promptrun.Recording {
		return &promptrun.Recording{
			DB: db, Placement: promptrun.Placement{Sessions: hostTree()},
			Origin: "gavel.todos", SpecProfile: "implement", AdmissionKey: "admission-" + uuid.NewString(),
			PromptMarkdown: "fix the build", VerificationMarkdown: "## Acceptance Criteria\n- builds",
			Runtime: database.PromptRunRuntime{Mode: "run", Driver: "cmux-claude",
				Requested: database.PromptRunRuntimeSelection{Provider: "anthropic", Mode: "agent", Model: "claude-sonnet-5"}},
			Link: link,
		}
	}

	It("admits the plain spec with its trace as metadata before the provider is called, then Link sees the run", func(ctx SpecContext) {
		provider := &eventsProvider{events: answeredTurn(uuid.NewString())}
		var linked *database.PromptRun
		rec := recording(func(ctx context.Context, tx *database.DB, run *database.PromptRun) error {
			Expect(provider.Calls()).To(BeZero(), "admission precedes dispatch")
			stored, err := tx.GetPromptRun(ctx, run.ID)
			Expect(err).NotTo(HaveOccurred(), "the run is visible inside the admission transaction")
			linked = stored
			return nil
		})
		presets := &runtimeprofiles.PresetResolution{Presets: []runtimeprofiles.Preset{{Name: "fast"}}}

		res, err := promptrun.Run(ctx, promptrun.Input{
			Resolved: resolved, RuntimePresets: presets, Provider: provider, Timeout: testTimeout, Record: rec,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(linked).NotTo(BeNil())
		Expect(res.PromptRunID).To(Equal(linked.ID))
		Expect(linked.State).To(Equal(database.PromptRunStatePending))
		Expect(linked.RenderedSpec).To(HaveKeyWithValue("prompt", map[string]any{"user": "fix the build"}))
		Expect(linked.RenderedSpec).NotTo(HaveKey("input"), "rendered_spec is the plain spec, not a render envelope")
		Expect(linked.Metadata).To(HaveKey("specTrace"))
		Expect(linked.Metadata["specTrace"]).To(HaveLen(2))
		Expect(linked.Metadata).To(HaveKeyWithValue("specWarnings", []any{"effort defaulted to high"}))
		Expect(linked.Metadata).To(HaveKey("runtimePresets"))
		Expect(linked.Metadata).NotTo(HaveKey("runtimeProfile"), "an unused catalog selection is omitted, not recorded empty")
		Expect(linked.Origin).To(Equal("gavel.todos"))
		Expect(linked.SpecProfile).To(Equal("implement"))
		Expect(linked.VerificationMarkdown).To(Equal("## Acceptance Criteria\n- builds"))
		Expect(linked.SessionID).To(Equal(rec.Placement.Sessions[1].ID), "the run is admitted on the last session of the placement tree")
	})

	It("rolls back the sessions and the run when Link fails, and never dispatches", func(ctx SpecContext) {
		provider := &eventsProvider{events: answeredTurn(uuid.NewString())}
		var runID uuid.UUID
		rec := recording(func(_ context.Context, _ *database.DB, run *database.PromptRun) error {
			runID = run.ID
			return errors.New("todo is no longer runnable")
		})

		_, err := promptrun.Run(ctx, promptrun.Input{Resolved: resolved, Provider: provider, Timeout: testTimeout, Record: rec})
		Expect(err).To(MatchError(ContainSubstring("todo is no longer runnable")))
		Expect(provider.Calls()).To(BeZero())
		_, err = db.GetPromptRun(ctx, runID)
		Expect(err).To(MatchError(database.ErrPromptRunNotFound))
		_, err = db.GetSession(ctx, rec.Placement.Sessions[0].ID)
		Expect(err).To(MatchError(database.ErrSessionNotFound))
	})

	It("rejects an approval opt-in without an event sink before admitting a run", func(ctx SpecContext) {
		rec := recording(nil)
		rec.PromptRunID = uuid.New()
		_, err := promptrun.Run(ctx, promptrun.Input{
			Resolved: resolved, Record: rec, Timeout: testTimeout,
			Approvals: &promptrun.ApprovalOptions{RequestedBy: "dashboard"},
		})
		Expect(err).To(MatchError(ContainSubstring("OnEvent")))
		_, err = db.GetPromptRun(ctx, rec.PromptRunID)
		Expect(err).To(MatchError(database.ErrPromptRunNotFound))
	})

	It("records start, the transcript binding, iterations and a succeeded finish", func(ctx SpecContext) {
		providerSessionID := uuid.NewString()
		provider := &eventsProvider{events: answeredTurn(providerSessionID)}
		rec := recording(nil)

		res, err := promptrun.Run(ctx, promptrun.Input{Resolved: resolved, Provider: provider, Timeout: testTimeout, Record: rec})
		Expect(err).NotTo(HaveOccurred())

		run, err := db.GetPromptRun(ctx, res.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateSucceeded))
		Expect(run.Phase).To(Equal(database.PromptRunPhaseFinished))
		Expect(run.ResultText).To(Equal("done"))
		Expect(run.StartedAt).NotTo(BeNil())
		Expect(run.Runtime.Resolved).To(Equal(database.PromptRunRuntimeSelection{
			Provider: "anthropic", Mode: "agent", Model: "claude-sonnet-5", Effort: "high",
		}))
		Expect(run.Runtime.Driver).To(Equal("cmux-claude"), "the host's requested runtime survives the start")

		admission, err := db.GetSession(ctx, run.SessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(admission.ProviderSessionID).To(Equal(providerSessionID))
		Expect(admission.CWD).To(Equal(cwd), "the admission session records where the agent ran")
		Expect(run.ExecutionSessionID).NotTo(BeNil())
		transcript, err := db.GetTranscriptSession(ctx, admission.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(transcript.ID).To(Equal(*run.ExecutionSessionID))
		Expect(transcript.Source).To(Equal("claude"), "the source comes from the executing runtime")
		Expect(transcript.ProviderSessionID).To(Equal(providerSessionID))

		iterations, err := db.ListPromptRunIterations(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(iterations).To(HaveLen(1))
		Expect(iterations[0].State).To(Equal(database.PromptRunIterationStateSucceeded))
	})

	It("records a failed verification with its progress, phase and reason", func(ctx SpecContext) {
		verify.Register(verify.KindFixture, func(_ context.Context, _ api.Verify, _ verify.Options) ([]*verify.Plugin, error) {
			return []*verify.Plugin{verify.New("fixture:acceptance", &failingVerifier{})}, nil
		})
		resolved.Spec.Workflow = &api.Workflow{Verify: &api.Verify{Fixture: "acceptance"}}
		provider := &eventsProvider{events: answeredTurn(uuid.NewString())}
		var progressed []api.VerifyReport

		res, err := promptrun.Run(ctx, promptrun.Input{
			Resolved: resolved, Provider: provider, Timeout: testTimeout, Record: recording(nil),
			Verify: verify.Options{Progress: func(report api.VerifyReport) { progressed = append(progressed, report) }},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(progressed).To(HaveLen(1), "the caller's progress tap still hears every snapshot")

		run, err := db.GetPromptRun(ctx, res.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateFailed))
		Expect(run.Phase).To(Equal(database.PromptRunPhaseVerify))
		Expect(run.Error).To(Equal("acceptance criteria unmet"))
		iterations, err := db.ListPromptRunIterations(ctx, run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(iterations).To(HaveLen(1))
		Expect(iterations[0].State).To(Equal(database.PromptRunIterationStateFailed),
			"the finished record replaces the in-flight running row")
	})

	It("leaves a run that ended on a question waiting", func(ctx SpecContext) {
		providerSessionID := uuid.NewString()
		provider := &eventsProvider{events: []ai.Event{
			{Kind: ai.EventSystem, SessionID: providerSessionID},
			{Kind: ai.EventToolUse, Tool: "AskUserQuestion", Input: map[string]any{"question": "Which database?"}},
			{Kind: ai.EventText, Text: "Which database?"},
			{Kind: ai.EventResult, Success: true},
		}}

		res, err := promptrun.Run(ctx, promptrun.Input{Resolved: resolved, Provider: provider, Timeout: testTimeout, Record: recording(nil)})
		Expect(err).NotTo(HaveOccurred())
		run, err := db.GetPromptRun(ctx, res.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateWaiting))
		Expect(run.Phase).To(Equal(database.PromptRunPhaseGenerate))
	})

	It("lets the host classify the outcome", func(ctx SpecContext) {
		provider := &eventsProvider{events: answeredTurn(uuid.NewString())}
		rec := recording(nil)
		rec.Outcome = func(result promptrun.Result, runErr error, stopped bool) (promptrun.Outcome, error) {
			outcome, err := promptrun.DefaultOutcome(result, runErr, stopped)
			outcome.State, outcome.Phase = database.PromptRunStateWaiting, database.PromptRunPhaseGenerate
			return outcome, err
		}

		res, err := promptrun.Run(ctx, promptrun.Input{Resolved: resolved, Provider: provider, Timeout: testTimeout, Record: rec})
		Expect(err).NotTo(HaveOccurred())
		run, err := db.GetPromptRun(ctx, res.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateWaiting))
		Expect(run.ResultText).To(Equal("done"))
	})

	It("records a run stopped by its caller as cancelled", func(ctx SpecContext) {
		provider := &eventsProvider{events: []ai.Event{{Kind: ai.EventSystem, SessionID: uuid.NewString()}}, block: true}
		runCtx, cancel := context.WithCancel(ctx)
		rec := recording(nil)
		rec.Link = func(context.Context, *database.DB, *database.PromptRun) error { return nil }
		go func() {
			defer GinkgoRecover()
			Eventually(provider.Calls).Should(Equal(1))
			cancel()
		}()

		res, err := promptrun.Run(runCtx, promptrun.Input{Resolved: resolved, Provider: provider, Timeout: testTimeout, Record: rec})
		Expect(err).To(HaveOccurred())
		run, getErr := db.GetPromptRun(ctx, res.PromptRunID)
		Expect(getErr).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateCancelled))
		Expect(run.Error).NotTo(BeEmpty())
	})

	It("writes the run's notices onto the transcript it bound", func(ctx SpecContext) {
		provider := &eventsProvider{events: answeredTurn(uuid.NewString())}
		resolved.Spec.Workflow = &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}

		res, err := promptrun.Run(ctx, promptrun.Input{Resolved: resolved, Provider: provider, Timeout: testTimeout, Record: recording(nil)})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Response.Workspace.Notices).NotTo(BeEmpty(), "the verify command reports a notice")
		run, err := db.GetPromptRun(ctx, res.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		messages, err := db.ListTranscriptMessages(ctx, database.TranscriptPage{SessionID: *run.ExecutionSessionID, Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		Expect(messages).To(HaveLen(len(res.Response.Workspace.Notices)))
	})

	It("persists the run workspace on finish", func(ctx SpecContext) {
		worktree := &api.WorktreeState{
			Repo: cwd, Path: cwd + "/worktrees/shell-abc", Branch: "shell/abc",
			Base: "1111111111111111111111111111111111111111", Setup: "2222222222222222222222222222222222222222",
			Head: "3333333333333333333333333333333333333333", Removed: true,
		}
		commit := api.CommitRecord{SHA: worktree.Head, Message: "feat: fix the build"}
		hook := &workspaceHook{worktree: worktree, commit: commit}

		res, err := promptrun.Run(ctx, promptrun.Input{
			Resolved: resolved, Provider: &eventsProvider{events: answeredTurn(uuid.NewString())},
			Timeout: testTimeout, Record: recording(nil), Hooks: []any{hook},
		})
		Expect(err).NotTo(HaveOccurred())

		want := &api.WorkspaceRecord{Cwd: cwd, Worktree: worktree, Commits: []api.CommitRecord{commit}}
		run, err := db.GetPromptRun(ctx, res.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.Workspace).To(Equal(want), "notices, diff and metadata stay off the durable record")
		overview, err := db.GetPromptRunOverview(ctx, res.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(overview.Workspace).To(Equal(want), "the overview carries the workspace through run.*")
	})

	Describe("Admit, for a host that admits ahead of dispatch", func() {
		It("files the pending run with its plain spec and trace and runs Link, dispatching nothing", func(ctx SpecContext) {
			provider := &eventsProvider{events: answeredTurn(uuid.NewString())}
			var linked uuid.UUID
			rec := recording(func(_ context.Context, _ *database.DB, run *database.PromptRun) error {
				linked = run.ID
				return nil
			})

			run, err := promptrun.Admit(ctx, promptrun.Input{Resolved: resolved, Provider: provider, Record: rec})

			Expect(err).NotTo(HaveOccurred())
			Expect(provider.Calls()).To(BeZero())
			Expect(linked).To(Equal(run.ID))
			stored, err := db.GetPromptRun(ctx, run.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(stored.State).To(Equal(database.PromptRunStatePending))
			Expect(stored.SessionID).To(Equal(rec.Placement.Sessions[1].ID))
			Expect(stored.RenderedSpec).To(HaveKeyWithValue("prompt", map[string]any{"user": "fix the build"}))
			Expect(stored.Metadata["specTrace"]).To(HaveLen(2))
		})

		It("resolves the run already admitted when the admission key is replayed", func(ctx SpecContext) {
			rec := recording(nil)
			first, err := promptrun.Admit(ctx, promptrun.Input{Resolved: resolved, Record: rec})
			Expect(err).NotTo(HaveOccurred())

			replayed, err := promptrun.Admit(ctx, promptrun.Input{Resolved: resolved, Record: rec})

			Expect(err).NotTo(HaveOccurred())
			Expect(replayed.ID).To(Equal(first.ID))
		})

		It("refuses a replay that resolves a finished run", func(ctx SpecContext) {
			rec := recording(nil)
			first, err := promptrun.Admit(ctx, promptrun.Input{Resolved: resolved, Record: rec})
			Expect(err).NotTo(HaveOccurred())
			_, err = promptrun.Fail(ctx, db, first.ID, "dispatcher exited")
			Expect(err).NotTo(HaveOccurred())

			_, err = promptrun.Admit(ctx, promptrun.Input{Resolved: resolved, Record: rec})

			Expect(err).To(MatchError(promptrun.ErrRunFinished))
		})

		It("requires a recording to admit", func(ctx SpecContext) {
			_, err := promptrun.Admit(ctx, promptrun.Input{Resolved: resolved})
			Expect(err).To(MatchError(ContainSubstring("recording")))
		})
	})

	It("fails a recording with neither an existing session nor a tree to place it in", func(ctx SpecContext) {
		rec := recording(nil)
		rec.Placement = promptrun.Placement{}
		_, err := promptrun.Run(ctx, promptrun.Input{Resolved: resolved, Provider: &eventsProvider{}, Timeout: testTimeout, Record: rec})
		Expect(err).To(MatchError(ContainSubstring("placement")))
	})
})
