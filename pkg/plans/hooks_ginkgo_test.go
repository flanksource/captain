package plans_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/plans"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	tldrHook   = "plan-tldr"
	tldrText   = "**TL;DR** add a cache"
	notesHook  = "notes"
	notesText  = "reviewed by bot"
)

func writePrompt(dir, name, frontmatter string) {
	Expect(os.WriteFile(filepath.Join(dir, name+".prompt"),
		[]byte("---\n"+frontmatter+"\n---\n{{role \"user\"}}\n{{{plan}}}\n"), 0o600)).To(Succeed())
}

func hookNames(hooks []plans.Hook) map[string]promptlib.HookMode {
	names := map[string]promptlib.HookMode{}
	for _, hook := range hooks {
		names[hook.Name] = hook.Mode
	}
	return names
}

// stubHook returns a hook that answers output and counts its calls.
func stubHook(name string, mode promptlib.HookMode, output string, calls *int) plans.Hook {
	return plans.Hook{Name: name, Mode: mode, Source: "test", Run: func(_ context.Context, input plans.HookInput) (string, error) {
		*calls++
		Expect(input.Markdown).NotTo(ContainSubstring("captain:hook"))
		return output, nil
	}}
}

var _ = Describe("plan hooks", func() {
	Describe("DiscoverHooks", func() {
		It("ships the embedded plan-tldr prepend hook", func() {
			hooks, err := plans.DiscoverHooks()
			Expect(err).NotTo(HaveOccurred())
			Expect(hookNames(hooks)).To(Equal(map[string]promptlib.HookMode{tldrHook: promptlib.HookPrepend}))
		})

		It("lets a later directory shadow, disable and add hooks while ignoring other events", func() {
			dir := GinkgoT().TempDir()
			writePrompt(dir, tldrHook, "hook: {disabled: true}")
			writePrompt(dir, notesHook, "model: api:haiku\nhook: {on: plan.revision, mode: append}")
			writePrompt(dir, "other-event", "hook: {on: session.end, mode: append}")
			writePrompt(dir, "plain", "model: api:haiku")
			hooks, err := plans.DiscoverHooks(dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(hookNames(hooks)).To(Equal(map[string]promptlib.HookMode{notesHook: promptlib.HookAppend}))
		})

		DescribeTable("rejects invalid hook frontmatter",
			func(frontmatter, message string) {
				dir := GinkgoT().TempDir()
				writePrompt(dir, "broken", frontmatter)
				_, err := plans.DiscoverHooks(dir)
				Expect(err).To(MatchError(ContainSubstring(message)))
			},
			Entry("unknown mode", "hook: {on: plan.revision, mode: overwrite}", `mode "overwrite"`),
			Entry("missing event", "hook: {mode: prepend}", "`on` is required"),
		)
	})

	Describe("Blocks and StripBlocks", func() {
		It("extracts and removes hook blocks", func() {
			markdown := "<!-- captain:hook:plan-tldr -->\n" + tldrText + "\n<!-- /captain:hook:plan-tldr -->\n\n" + firstMarkdown
			Expect(plans.Blocks(markdown)).To(Equal(map[string]string{tldrHook: tldrText}))
			Expect(plans.StripBlocks(markdown)).To(Equal(firstMarkdown))
		})
	})

	Describe("plans.Save with hooks", func() {
		var (
			db        *database.DB
			sessionID uuid.UUID
			planID    uuid.UUID
		)

		BeforeEach(func(ctx SpecContext) {
			db, sessionID = openPlanDB(ctx, "plans-hooks")
			planID = uuid.New()
		})

		withID := func(markdown string) plans.SaveInput {
			input := saveInput(sessionID, markdown)
			input.Plan.ID = planID
			return input
		}

		It("prepends and appends marked blocks around the authored body", func(ctx SpecContext) {
			var calls int
			outcome, err := plans.Save(ctx, db, withID(firstMarkdown), plans.SaveOptions{Hooks: []plans.Hook{
				stubHook(notesHook, promptlib.HookAppend, notesText, &calls),
				stubHook(tldrHook, promptlib.HookPrepend, tldrText, &calls),
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Revision.PlanMarkdown).To(Equal(
				"<!-- captain:hook:plan-tldr -->\n" + tldrText + "\n<!-- /captain:hook:plan-tldr -->\n\n" +
					firstMarkdown + "\n\n<!-- captain:hook:notes -->\n" + notesText + "\n<!-- /captain:hook:notes -->"))
			Expect(calls).To(Equal(2))
		})

		It("replaces the body", func(ctx SpecContext) {
			var calls int
			outcome, err := plans.Save(ctx, db, withID(firstMarkdown), plans.SaveOptions{Hooks: []plans.Hook{
				stubHook("rewrite", promptlib.HookReplace, secondMarkdown, &calls),
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Revision.PlanMarkdown).To(Equal(secondMarkdown))
		})

		It("reuses the prior revision without running hooks when the body is unchanged", func(ctx SpecContext) {
			var calls int
			hooks := []plans.Hook{stubHook(tldrHook, promptlib.HookPrepend, tldrText, &calls)}
			first, err := plans.Save(ctx, db, withID(firstMarkdown), plans.SaveOptions{Hooks: hooks})
			Expect(err).NotTo(HaveOccurred())

			replayed, err := plans.Save(ctx, db, withID(firstMarkdown+"\n"), plans.SaveOptions{Hooks: hooks})
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{calls, replayed.Changed, replayed.Revision.ID}).To(Equal([]any{1, false, first.Revision.ID}))

			revised, err := plans.Save(ctx, db, withID(secondMarkdown), plans.SaveOptions{Hooks: hooks})
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{calls, plans.StripBlocks(revised.Revision.PlanMarkdown)}).To(Equal([]any{2, secondMarkdown}))
		})

		It("persists nothing when a hook fails", func(ctx SpecContext) {
			hookErr := errors.New("model unavailable")
			failing := plans.Hook{Name: tldrHook, Mode: promptlib.HookPrepend, Source: "test",
				Run: func(context.Context, plans.HookInput) (string, error) { return "", hookErr }}
			_, err := plans.Save(ctx, db, withID(firstMarkdown), plans.SaveOptions{Hooks: []plans.Hook{failing}})
			Expect(err).To(MatchError(hookErr))
			_, err = db.GetPlan(ctx, planID)
			Expect(err).To(MatchError(database.ErrPlanNotFound))
		})
	})
})
