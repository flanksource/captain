package commit

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("the stage mode a stage-less policy resolves", func() {
	hookContext := func(ws *api.Workspace) *agent.HookContext {
		return &agent.HookContext{Context: context.Background(), Request: &ai.Request{}, Response: &ai.Response{Workspace: ws}}
	}
	policy := api.Commit{On: api.CommitOnRun, Message: "feat: isolated"}

	It("a Worktree workspace counts as isolated", func() {
		ws := &api.Workspace{Cwd: "/work/wt", Worktree: &api.WorktreeState{Path: "/work/wt", Branch: "shell/abc"}}
		Expect(New(policy).stageMode(hookContext(ws))).To(Equal(api.CommitStageWorktree))
	})

	It("a workspace without a Worktree is the caller's shared tree", func() {
		ws := &api.Workspace{Cwd: "/work/repo", Repo: "/work/repo"}
		Expect(New(policy).stageMode(hookContext(ws))).To(Equal(api.CommitStageChanged))
	})
})
