package aichat

import (
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

// Three branches build the same session aggregate — the database branch, a
// transcript re-parse, and a prompt run — so anything only one of them maps
// appears or vanishes depending on which served the request. An ingested session
// takes the database branch, which is why its changed files, git branch and
// context window used to disappear the moment it gained messages.
var _ = ginkgo.Describe("applyOverviewMetadata", func() {
	contextTokens := int64(48_000)
	windowTokens := int64(200_000)
	freePercent := 76

	overview := func() database.SessionOverview {
		return database.SessionOverview{
			ID:  uuid.MustParse("860305d7-e8cd-41b8-b5ee-332dc74d4a41"),
			Git: json.RawMessage(`{"branch":"feat/todo-prompt-runs","commit":"0c6f277","worktree":"/tmp/wt"}`),
			Metadata: json.RawMessage(`{
				"model": "gpt-5-codex",
				"provider": "openai",
				"files": {"read": ["todos/plans.go"], "written": ["todos/outcome.go", "todos/provider.go"]},
				"todos": [{"text": "ship it", "status": "pending"}],
				"plan": {"path": "/plans/stored.md", "slug": "stored"},
				"tags": ["ignored-sibling-key"]
			}`)}
	}

	project := func(overview database.SessionOverview) *session.Session {
		detail := &session.Session{}
		applyOverviewMetadata(overview, detail)
		return detail
	}

	ginkgo.It("projects the changed files stored by the monitor", func() {
		Expect(project(overview()).Files).To(Equal(session.ChangedFiles{
			Read:    []string{"todos/plans.go"},
			Written: []string{"todos/outcome.go", "todos/provider.go"},
		}))
	})

	ginkgo.It("projects todos, which no reader previously declared", func() {
		Expect(project(overview()).Todos).To(Equal(
			[]tools.TodoItem{{Text: "ship it", Status: "pending"}}))
	})

	ginkgo.It("projects the git state the row already carries", func() {
		Expect(project(overview()).Git).To(Equal(session.GitState{
			Branch: "feat/todo-prompt-runs", Commit: "0c6f277", Worktree: "/tmp/wt",
		}))
	})

	ginkgo.It("projects the context window from the overview's own columns", func() {
		row := overview()
		row.ContextTokens, row.ContextWindowTokens, row.ContextFreePercent = &contextTokens, &windowTokens, &freePercent

		Expect(project(row).Context).To(Equal(&session.Context{
			UsedTokens: 48_000, WindowTokens: 200_000, FreePercent: 76,
		}))
	})

	ginkgo.It("projects the stored plan when the branch found none", func() {
		Expect(project(overview()).Plan).To(Equal(&session.Plan{Path: "/plans/stored.md", Slug: "stored"}))
	})

	ginkgo.DescribeTable("leaves a value the branch already resolved alone",
		func(seed *session.Session, assert func(*session.Session)) {
			applyOverviewMetadata(overview(), seed)
			assert(seed)
		},
		ginkgo.Entry("transcript-derived files",
			&session.Session{Files: session.ChangedFiles{Written: []string{"from/transcript.go"}}},
			func(s *session.Session) {
				Expect(s.Files.Written).To(Equal([]string{"from/transcript.go"}))
				Expect(s.Files.Read).To(BeEmpty())
			}),
		ginkgo.Entry("transcript-derived git",
			&session.Session{Git: session.GitState{Branch: "main"}},
			func(s *session.Session) { Expect(s.Git.Branch).To(Equal("main")) }),
		ginkgo.Entry("model resolved from the overview column",
			&session.Session{Model: "claude-opus-5"},
			func(s *session.Session) { Expect(s.Model).To(Equal("claude-opus-5")) }),
		ginkgo.Entry("a plan the branch already recovered",
			&session.Session{Plan: &session.Plan{Slug: "from-transcript"}},
			func(s *session.Session) { Expect(s.Plan.Slug).To(Equal("from-transcript")) }),
	)

	ginkgo.It("falls back to the stored model and provider when the columns are empty", func() {
		detail := project(overview())

		Expect(detail.Model).To(Equal("gpt-5-codex"))
		Expect(detail.Provider).To(Equal("openai"))
	})

	ginkgo.It("leaves approvals to the turn-request rows, not the transcript count", func() {
		// The stored copy counts every operational tool use as approved, which
		// reads as "200 approved" on a 200-tool-call session. applyRequestState
		// derives the real figure from captain_turn_requests.
		row := overview()
		row.Metadata = json.RawMessage(`{"approvals": {"approved": 200, "denied": 0}}`)

		Expect(project(row).Approvals).To(Equal(session.ApprovalStats{}))
	})

	ginkgo.DescribeTable("survives a row with nothing to project",
		func(mutate func(*database.SessionOverview)) {
			row := database.SessionOverview{ID: uuid.New()}
			mutate(&row)

			detail := project(row)

			Expect(detail.Files).To(Equal(session.ChangedFiles{}))
			Expect(detail.Git).To(Equal(session.GitState{}))
			Expect(detail.Context).To(BeNil())
			Expect(detail.Plan).To(BeNil())
		},
		ginkgo.Entry("no metadata or git at all", func(*database.SessionOverview) {}),
		ginkgo.Entry("empty json objects", func(r *database.SessionOverview) {
			r.Metadata, r.Git = json.RawMessage(`{}`), json.RawMessage(`{}`)
		}),
		ginkgo.Entry("malformed json", func(r *database.SessionOverview) {
			r.Metadata, r.Git = json.RawMessage(`{"files":`), json.RawMessage(`nope`)
		}),
	)
})
