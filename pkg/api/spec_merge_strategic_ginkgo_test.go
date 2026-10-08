package api

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Commit stanzas merge by phase, as a Kubernetes strategic merge patch keyed on
// `on`: a layer that names a phase says what it changes about that phase's
// policy, not what the whole policy is. A request that only asks for a commit at
// the end of the run must not strip the staging and gates a lower layer chose.
var _ = Describe("Strategic merge of workflow commits", func() {
	commits := func(stanzas ...Commit) Spec { return Spec{Workflow: &Workflow{Commits: stanzas}} }
	decoded := func(encoded string) Spec {
		var spec Spec
		Expect(json.Unmarshal([]byte(encoded), &spec)).To(Succeed())
		return spec
	}
	lifecycleRun := Commit{On: CommitOnRun, Stage: CommitStageWorktree, Gates: CommitGatesFull}

	DescribeTable("merges an override's stanzas into the base's by phase",
		func(base []Commit, override Spec, want []Commit) {
			Expect(commits(base...).Merge(override).Workflow.Commits).To(Equal(want))
		},
		Entry("a bare phase keeps the inherited staging and gates",
			[]Commit{lifecycleRun}, commits(Commit{On: CommitOnRun}), []Commit{lifecycleRun}),
		Entry("a decoded bare phase keeps the inherited staging and gates",
			[]Commit{lifecycleRun}, decoded(`{"workflow":{"commits":[{"on":"run"}]}}`), []Commit{lifecycleRun}),
		Entry("a stanza that leaves its phase implicit meets one that names run",
			[]Commit{{Stage: CommitStageWorktree}}, commits(Commit{On: CommitOnRun, Gates: CommitGatesFull}), []Commit{lifecycleRun}),
		Entry("a decoded stanza meets a base stanza that leaves its phase implicit",
			[]Commit{{Stage: CommitStageWorktree}}, decoded(`{"workflow":{"commits":[{"on":"run","gates":"full"}]}}`), []Commit{lifecycleRun}),
		Entry("a message changes only the message",
			[]Commit{lifecycleRun}, commits(Commit{On: CommitOnRun, Message: "apply"}),
			[]Commit{{On: CommitOnRun, Stage: CommitStageWorktree, Gates: CommitGatesFull, Message: "apply"}}),
		Entry("a decoded message changes only the message",
			[]Commit{lifecycleRun}, decoded(`{"workflow":{"commits":[{"on":"run","message":"apply"}]}}`),
			[]Commit{{On: CommitOnRun, Stage: CommitStageWorktree, Gates: CommitGatesFull, Message: "apply"}}),
		Entry("distinct phases are both kept, the override's first",
			[]Commit{{On: CommitOnTurn, Mode: CommitModeCommit}}, commits(Commit{On: CommitOnRun}),
			[]Commit{{On: CommitOnRun}, {On: CommitOnTurn, Mode: CommitModeCommit}}),
		Entry("decoded distinct phases are both kept, the override's first",
			[]Commit{{On: CommitOnTurn, Mode: CommitModeCommit}}, decoded(`{"workflow":{"commits":[{"on":"run"}]}}`),
			[]Commit{{On: CommitOnRun}, {On: CommitOnTurn, Mode: CommitModeCommit}}),
		Entry("an explicitly emptied list clears every inherited stanza",
			[]Commit{lifecycleRun}, decoded(`{"workflow":{"commits":[]}}`), []Commit{}),
		Entry("a Go-constructed empty list states nothing and keeps the base",
			[]Commit{lifecycleRun}, commits(), []Commit{lifecycleRun}),
		// The decoded "stage": "" is an explicit zero, which replaces what it names —
		// on the override's run stanza, wherever the merge put it, never on the
		// stanza that happens to share its position in the override.
		Entry("an explicit zero lands on the stanza sharing its phase, not its position",
			[]Commit{{On: CommitOnAgent, Stage: CommitStageChanged}, {On: CommitOnRun, Stage: CommitStageWorktree}},
			decoded(`{"workflow":{"commits":[{"on":"turn"},{"on":"run","stage":""}]}}`),
			[]Commit{{On: CommitOnTurn}, {On: CommitOnAgent, Stage: CommitStageChanged}, {On: CommitOnRun}}),
	)

	It("resolves implicit phases on copies, leaving both operands as authored", func() {
		base, override := commits(Commit{Stage: CommitStageWorktree}), commits(Commit{Gates: CommitGatesFull})

		base.Merge(override)

		Expect(base.Workflow.Commits).To(Equal([]Commit{{Stage: CommitStageWorktree}}))
		Expect(override.Workflow.Commits).To(Equal([]Commit{{Gates: CommitGatesFull}}))
	})

	It("re-addresses an inherited explicit marker to its stanza's merged position", func() {
		base := decoded(`{"workflow":{"commits":[{"on":"run","dryRun":false}]}}`)

		merged := base.Merge(commits(Commit{On: CommitOnTurn}))

		Expect(merged.Workflow.Commits).To(Equal([]Commit{{On: CommitOnTurn}, {On: CommitOnRun}}))
		Expect(merged.Explicit).To(HaveKey("/workflow/commits/1/dryRun"))
		Expect(merged.Explicit).NotTo(HaveKey("/workflow/commits/0/dryRun"))
	})

	Describe("resolved through spec layers", func() {
		lifecycle := func(stanzas ...Commit) SpecLayer {
			return SpecLayer{Name: "lifecycle step run", Scope: SpecLayerSurface, Spec: commits(stanzas...)}
		}
		provenance := func(resolved ResolvedSpec, path string) string {
			Expect(resolved.Provenance).To(HaveKey(path))
			return resolved.Provenance[path].Source.Name
		}

		It("keeps the lower layer's policy and attributes each field to the layer that set it", func() {
			resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
				RequestSpecLayer("request", decoded(`{"workflow":{"commits":[{"on":"run"}]}}`)),
				lifecycle(lifecycleRun),
			}})

			Expect(err).NotTo(HaveOccurred())
			Expect(resolved.Spec.Workflow.Commits).To(Equal([]Commit{lifecycleRun}))
			Expect(provenance(resolved, "/workflow/commits/0/stage")).To(Equal("lifecycle step run"))
			Expect(provenance(resolved, "/workflow/commits/0/gates")).To(Equal("lifecycle step run"))
			Expect(provenance(resolved, "/workflow/commits/0/on")).To(Equal("request"))
		})

		It("moves a lower layer's provenance with its stanza when a new phase is added ahead of it", func() {
			resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
				lifecycle(lifecycleRun),
				RequestSpecLayer("request", commits(Commit{On: CommitOnTurn})),
			}})

			Expect(err).NotTo(HaveOccurred())
			Expect(resolved.Spec.Workflow.Commits).To(Equal([]Commit{{On: CommitOnTurn}, lifecycleRun}))
			Expect(provenance(resolved, "/workflow/commits/0/on")).To(Equal("request"))
			Expect(provenance(resolved, "/workflow/commits/1/on")).To(Equal("lifecycle step run"))
			Expect(provenance(resolved, "/workflow/commits/1/stage")).To(Equal("lifecycle step run"))
			Expect(resolved.Provenance).NotTo(HaveKey("/workflow/commits/0/stage"))
		})

		It("drops a lower layer's provenance when a later layer explicitly clears the list", func() {
			resolved, err := ResolveSpecLayers(ResolveSpecOptions{Layers: []SpecLayer{
				lifecycle(lifecycleRun),
				RequestSpecLayer("request", decoded(`{"workflow":{"commits":[]}}`)),
			}})

			Expect(err).NotTo(HaveOccurred())
			Expect(resolved.Spec.Workflow.Commits).To(BeEmpty())
			Expect(provenance(resolved, "/workflow/commits")).To(Equal("request"))
			Expect(resolved.Provenance).NotTo(HaveKey("/workflow/commits/0/stage"))
		})
	})
})
