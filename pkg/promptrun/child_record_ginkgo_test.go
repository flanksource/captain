package promptrun_test

import (
	"context"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
)

// freshSessionProvider answers every call in a provider session of its own, as
// a real parent and child run are.
type freshSessionProvider struct{ eventsProvider }

func (p *freshSessionProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	p.events = answeredTurn(uuid.NewString())
	return p.eventsProvider.ExecuteStream(ctx, req)
}

var _ = Describe("a recorded run's child prompt", func() {
	It("is filed as its own run under the parent run, in a child session of the parent's", func(ctx SpecContext) {
		db := openRecordDB()
		dir := GinkgoT().TempDir()
		child := filepath.Join(dir, "stage2.prompt")
		Expect(os.WriteFile(child, []byte("---\nmodel: deepseek-reasoner\n---\nPick a transaction for {{product}}."), 0o644)).To(Succeed())

		spec := api.Spec{
			Model:  api.Model{Name: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent},
			Prompt: api.Prompt{User: "classify", Variables: map[string]any{"product": "Annuity"}},
			Workflow: &api.Workflow{After: []api.AfterStep{{When: api.CommitWhenSuccess, HookAction: api.HookAction{
				Prompt: child, With: map[string]any{"product": "{{vars.product}}"},
			}}}},
		}
		spec.SetCwd(dir)
		rec := &promptrun.Recording{
			DB: db, Placement: promptrun.Placement{Sessions: hostTree()}, Origin: "captain", SpecProfile: "classify",
			Runtime: database.PromptRunRuntime{Mode: "run", Driver: "prompt"},
		}

		parent, err := promptrun.Run(ctx, promptrun.Input{
			Resolved: api.ResolvedSpec{Spec: spec}, Provider: &freshSessionProvider{},
			Timeout: testTimeout, Record: rec,
		})
		Expect(err).NotTo(HaveOccurred())

		parentRun, err := db.GetPromptRun(ctx, parent.PromptRunID)
		Expect(err).NotTo(HaveOccurred())
		children, err := db.ListPromptRuns(ctx, database.PromptRunFilter{ParentRunID: &parentRun.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(children).To(HaveLen(1))
		Expect(children[0].SpecProfile).To(Equal("stage2"))
		Expect(children[0].State).To(Equal(database.PromptRunStateSucceeded))
		Expect(children[0].RootSessionID).To(Equal(parentRun.RootSessionID))
		Expect(children[0].PromptMarkdown).To(Equal("Pick a transaction for Annuity."))

		session, err := db.GetSession(ctx, children[0].SessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(session.ParentSessionID).To(Equal(&parentRun.SessionID))
		Expect(session.ParentRelation).To(Equal(database.SessionParentRelationAgent))
	})
})
