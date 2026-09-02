package cli

import (
	"context"
	"path/filepath"
	"sync/atomic"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// runtimeCatalogFixture is a file-backed preset/profile catalog pinned on the
// context, next to an isolated ~/.captain.yaml and a local prompt directory.
type runtimeCatalogFixture struct {
	ctx      context.Context
	catalog  *runtimeprofiles.Catalog
	presets  runtimeprofiles.Source
	profiles runtimeprofiles.Source
}

// probeSource counts how often the catalog reaches into a source, so a test
// can prove a render never consulted the catalog.
type probeSource struct {
	runtimeprofiles.Source
	reads atomic.Int32
}

func (p *probeSource) Presets() runtimeprofiles.Store[runtimeprofiles.Preset, runtimeprofiles.PresetInput] {
	p.reads.Add(1)
	return p.Source.Presets()
}

func (p *probeSource) Profiles() runtimeprofiles.Store[runtimeprofiles.Profile, runtimeprofiles.ProfileInput] {
	p.reads.Add(1)
	return p.Source.Profiles()
}

func newRuntimeCatalogFixture() (runtimeCatalogFixture, *probeSource, *probeSource) {
	GinkgoHelper()
	captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
	DeferCleanup(captainconfig.SetPathForTesting, "")
	root := GinkgoT().TempDir()
	presets, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
		Kind: runtimeprofiles.KindPreset, Dir: filepath.Join(root, "presets"), Label: "test presets", Implicit: true,
	})
	Expect(err).NotTo(HaveOccurred())
	profiles, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
		Kind: runtimeprofiles.KindProfile, Dir: filepath.Join(root, "profiles"), Label: "test profiles", Implicit: true,
	})
	Expect(err).NotTo(HaveOccurred())
	presetProbe, profileProbe := &probeSource{Source: presets}, &probeSource{Source: profiles}
	catalog, err := runtimeprofiles.NewCatalog(presetProbe, profileProbe)
	Expect(err).NotTo(HaveOccurred())
	ctx := ContextWithPromptDirs(context.Background(), []string{GinkgoT().TempDir()})
	return runtimeCatalogFixture{
		ctx: ContextWithRuntimeCatalog(ctx, catalog), catalog: catalog, presets: presets, profiles: profiles,
	}, presetProbe, profileProbe
}

func (f runtimeCatalogFixture) preset(in runtimeprofiles.PresetInput) runtimeprofiles.Preset {
	GinkgoHelper()
	preset, err := f.catalog.CreatePreset(f.ctx, f.presets.Info().ID, in)
	Expect(err).NotTo(HaveOccurred())
	return preset
}

func (f runtimeCatalogFixture) profile(in runtimeprofiles.ProfileInput) runtimeprofiles.Profile {
	GinkgoHelper()
	profile, err := f.catalog.CreateProfile(f.ctx, f.profiles.Info().ID, in)
	Expect(err).NotTo(HaveOccurred())
	return profile
}

func (f runtimeCatalogFixture) prompt(name, content string) PromptDetail {
	GinkgoHelper()
	detail, err := writeNewLocalPrompt(f.ctx, PromptWriteRequest{Name: name, Content: content})
	Expect(err).NotTo(HaveOccurred())
	return detail
}

// reviewCatalog seeds the fixture with a global "Org" preset and a "Review"
// profile layering it under a profile-level budget.
func (f runtimeCatalogFixture) reviewCatalog() {
	GinkgoHelper()
	f.preset(runtimeprofiles.PresetInput{
		Name: "Org", Scope: api.SpecLayerGlobal,
		Spec: api.RuntimePresetSpec{
			Model:       api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAgent},
			Budget:      api.Budget{MaxTurns: 20},
			Permissions: api.Permissions{Mode: api.PermissionAcceptEdits},
			Memory:      api.Memory{SkipUser: true},
		},
	})
	f.profile(runtimeprofiles.ProfileInput{
		Name: "Review", Presets: []string{"org"}, Spec: api.Spec{Budget: api.Budget{MaxTurns: 10}},
	})
}

func traceNames(resolved api.ResolvedSpec) []string {
	names := make([]string, 0, len(resolved.Trace))
	for _, layer := range resolved.Trace {
		names = append(names, layer.Name)
	}
	return names
}

const layeredPromptSource = `---
name: Layered
permissions:
  mode: plan
budget:
  maxTurns: 7
---
{{role "user"}}
Review the diff.
`

var _ = Describe("prompt layers", func() {
	It("resolves profile < frontmatter < request and reports the trace", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.reviewCatalog()
		detail := f.prompt("layered", layeredPromptSource)

		rendered, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{
			RuntimeProfile: "review",
			Spec: &api.Spec{
				Model:  api.Model{Name: "gpt-4o", Mode: api.ModeAPI},
				Budget: api.Budget{MaxTurns: 3},
			},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Model).To(Equal("gpt-4o"), "the request replaces the profile's model")
		Expect(rendered.Provider).To(Equal(api.OpenAI.Name))
		Expect(rendered.Input.Budget.MaxTurns).To(Equal(3), "the request beats frontmatter and profile")
		Expect(rendered.Input.Permissions.Mode).To(Equal(api.PermissionPlan), "frontmatter beats the profile")
		Expect(rendered.Input.Memory.SkipUser).To(BeTrue(), "an unopposed preset value survives")
		Expect(traceNames(rendered.Resolution)).To(Equal([]string{"Org", "Review run spec", "layered.prompt", "render request"}))
		Expect(rendered.Resolution.Trace).To(HaveExactElements(
			HaveField("Source", api.SpecLayerSourcePreset),
			HaveField("Source", api.SpecLayerSourceProfile),
			HaveField("Source", api.SpecLayerSourcePrompt),
			HaveField("Source", api.SpecLayerSourceRequest),
		))
		Expect(rendered.Resolution.Trace).To(HaveExactElements(
			HaveField("Scope", api.SpecLayerGlobal),
			HaveField("Scope", api.SpecLayerSurface),
			HaveField("Scope", api.SpecLayerSurface),
			HaveField("Scope", api.SpecLayerUser),
		))
		Expect(rendered.Resolution.Spec).To(Equal(rendered.Input))
	})

	It("lets the frontmatter override the profile when the request is silent", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.reviewCatalog()
		detail := f.prompt("layered", layeredPromptSource)

		rendered, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{RuntimeProfile: "review"})

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Model).To(Equal("claude-sonnet-4-6"), "the profile supplies the model the frontmatter lacks")
		Expect(rendered.Mode).To(Equal(string(api.ModeAgent)))
		Expect(rendered.Input.Budget.MaxTurns).To(Equal(7))
		Expect(traceNames(rendered.Resolution)).To(Equal([]string{"Org", "Review run spec", "layered.prompt"}))
	})

	It("orders presets by scope so a user preset outranks the frontmatter but not the request", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.preset(runtimeprofiles.PresetInput{
			Name: "Personal", Scope: api.SpecLayerUser, Spec: api.RuntimePresetSpec{Budget: api.Budget{MaxTurns: 9}},
		})
		f.preset(runtimeprofiles.PresetInput{
			Name: "Org", Scope: api.SpecLayerGlobal,
			Spec: api.RuntimePresetSpec{Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAgent}},
		})
		f.profile(runtimeprofiles.ProfileInput{Name: "Review", Presets: []string{"personal", "org"}})
		detail := f.prompt("layered", layeredPromptSource)

		byPreset, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{RuntimeProfile: "review"})
		Expect(err).NotTo(HaveOccurred())
		byRequest, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{
			RuntimeProfile: "review", Spec: &api.Spec{Budget: api.Budget{MaxTurns: 3}},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(traceNames(byPreset.Resolution)).To(Equal([]string{"Org", "Review run spec", "layered.prompt", "Personal"}))
		Expect(byPreset.Input.Budget.MaxTurns).To(Equal(9), "a user preset outranks the surface frontmatter")
		Expect(traceNames(byRequest.Resolution)).To(Equal([]string{"Org", "Review run spec", "layered.prompt", "Personal", "render request"}))
		Expect(byRequest.Input.Budget.MaxTurns).To(Equal(3), "the request is appended last and wins the user-scope tie")
	})

	It("selects the profile pinned in the frontmatter and seeds the run request with it", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.reviewCatalog()
		detail := f.prompt("pinned", "---\nname: Pinned\nruntimeProfile: review\n---\n{{role \"user\"}}\nReview.\n")

		rendered, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{})

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Model).To(Equal("claude-sonnet-4-6"))
		Expect(rendered.Input.Budget.MaxTurns).To(Equal(10))
		Expect(traceNames(rendered.Resolution)).To(Equal([]string{"Org", "Review run spec", "pinned.prompt"}))
		Expect(detail.RuntimeProfile).To(Equal("review"))
		Expect(detail.Run.RuntimeProfile).To(Equal("review"))
	})

	It("lets a request profile override the frontmatter pin", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.reviewCatalog()
		f.profile(runtimeprofiles.ProfileInput{Name: "Plan", Spec: api.Spec{
			Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAgent}, Budget: api.Budget{MaxTurns: 4},
		}})
		detail := f.prompt("pinned", "---\nname: Pinned\nruntimeProfile: review\n---\n{{role \"user\"}}\nReview.\n")

		rendered, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{RuntimeProfile: "plan"})

		Expect(err).NotTo(HaveOccurred())
		Expect(traceNames(rendered.Resolution)).To(Equal([]string{"Plan run spec", "pinned.prompt"}))
		Expect(rendered.Input.Budget.MaxTurns).To(Equal(4))
	})

	It("fails loudly naming a profile reference that resolves nowhere", func() {
		f, _, _ := newRuntimeCatalogFixture()
		detail := f.prompt("layered", layeredPromptSource)

		_, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{RuntimeProfile: "nope"})

		Expect(err).To(MatchError(runtimeprofiles.ErrNotFound))
		Expect(err).To(MatchError(ContainSubstring(`runtime profile "nope"`)))
	})

	It("folds enabled permission skills into the memory skill directories", func() {
		f, _, _ := newRuntimeCatalogFixture()
		detail := f.prompt("skills", "---\nname: Skills\nmodel: claude-sonnet-4-6\npermissions:\n  skills:\n    /team-skills: enabled\n    /retired: disabled\n---\n{{role \"user\"}}\nGo.\n")

		rendered, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{
			Spec: &api.Spec{Memory: api.Memory{Skills: []string{"/mine", "/team-skills"}}},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Input.Memory.Skills).To(Equal([]string{"/mine", "/team-skills"}))
	})

	It("layers the ephemeral request over a selected profile", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.reviewCatalog()

		rendered, err := renderPrompt(f.ctx, "", PromptRenderRequest{
			RuntimeProfile: "review", Spec: &api.Spec{Prompt: api.Prompt{User: "Draft a plan"}},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Model).To(Equal("claude-sonnet-4-6"))
		Expect(rendered.Input.Prompt.Source).To(Equal("<ephemeral>"))
		Expect(traceNames(rendered.Resolution)).To(Equal([]string{"Org", "Review run spec", "scratch.prompt", "render request"}))
	})

	It("layers --runtime-profile beneath the CLI flags", func() {
		f, _, _ := newRuntimeCatalogFixture()
		f.reviewCatalog()
		detail := f.prompt("layered", layeredPromptSource)
		opts := AIPromptOptions{RuntimeProfile: "review"}
		opts.MaxTurns = 2

		rendered, err := renderPromptCLI(f.ctx, detail.Path, opts, "", "")

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Model).To(Equal("claude-sonnet-4-6"), "the profile supplies the model")
		Expect(rendered.Input.Budget.MaxTurns).To(Equal(2), "the flag stays above the resolved layers")
		Expect(rendered.Input.Permissions.Mode).To(Equal(api.PermissionPlan))
		Expect(rendered.Input.Memory.SkipUser).To(BeTrue())
		Expect(traceNames(rendered.Resolution)).To(Equal([]string{"Org", "Review run spec", "layered.prompt"}))
	})

	It("never consults the catalog for a render without a profile reference or pin", func() {
		f, presetProbe, profileProbe := newRuntimeCatalogFixture()
		f.reviewCatalog()
		presetProbe.reads.Store(0)
		profileProbe.reads.Store(0)
		detail := f.prompt("plain", "---\nname: Plain\nmodel: claude-sonnet-4-6\n---\n{{role \"user\"}}\nHi.\n")

		_, err := renderPrompt(f.ctx, detail.ID, PromptRenderRequest{Spec: &api.Spec{Budget: api.Budget{MaxTurns: 3}}})
		Expect(err).NotTo(HaveOccurred())
		_, err = renderPromptCLI(f.ctx, detail.Path, AIPromptOptions{}, "", "")
		Expect(err).NotTo(HaveOccurred())

		Expect(presetProbe.reads.Load()).To(BeZero())
		Expect(profileProbe.reads.Load()).To(BeZero())

		_, err = renderPrompt(f.ctx, detail.ID, PromptRenderRequest{RuntimeProfile: "review"})
		Expect(err).NotTo(HaveOccurred())
		Expect(profileProbe.reads.Load()).NotTo(BeZero(), "the probe sees a render that does name a profile")
	})
})
