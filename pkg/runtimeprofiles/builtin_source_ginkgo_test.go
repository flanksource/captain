package runtimeprofiles

import (
	"errors"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// builtinFixture is the catalog fixture with the built-in source registered
// last, as NewDefaultCatalog does.
type builtinFixture struct {
	catalogFixture
	builtin Source
}

func newBuiltinFixture() builtinFixture {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	presets, err := NewFileSource(FileSourceOptions{Kind: KindPreset, Dir: filepath.Join(root, "presets"), Label: "team presets", Implicit: true})
	Expect(err).NotTo(HaveOccurred())
	otherPresets, err := NewFileSource(FileSourceOptions{Kind: KindPreset, Dir: filepath.Join(root, "repo-presets"), Label: "repo presets", Implicit: true})
	Expect(err).NotTo(HaveOccurred())
	profiles, err := NewFileSource(FileSourceOptions{Kind: KindProfile, Dir: filepath.Join(root, "profiles"), Label: "team profiles", Implicit: true})
	Expect(err).NotTo(HaveOccurred())
	db := newMemSource("db", SourceDB, true)
	builtin := NewBuiltinSource()
	catalog, err := NewCatalog(db, presets, otherPresets, profiles, builtin)
	Expect(err).NotTo(HaveOccurred())
	return builtinFixture{
		catalogFixture: catalogFixture{catalog: catalog, db: db, presets: presets, profiles: profiles},
		builtin:        builtin,
	}
}

func presetSummaries(presets []Preset) [][2]string {
	out := make([][2]string, 0, len(presets))
	for _, preset := range presets {
		out = append(out, [2]string{string(preset.Source.Kind), preset.Name})
	}
	return out
}

var builtinPlanID = EncodeID(KindPreset, BuiltinSourceID, "plan")

var _ = Describe("Built-in source", func() {
	It("is a read-only preset source", func() {
		Expect(NewBuiltinSource().Info()).To(Equal(SourceInfo{
			Kind: SourceBuiltin, ID: BuiltinSourceID, Label: "Built-in", Writable: false, Records: []Kind{KindPreset},
		}))
		Expect(NewBuiltinSource().Profiles()).To(BeNil())
	})

	It("ships Edit, Plan and Read-only as context presets with their permission postures", func(ctx SpecContext) {
		presets, err := NewBuiltinSource().Presets().List(ctx)
		Expect(err).NotTo(HaveOccurred())
		type posture struct {
			Key, Name string
			Scope     api.SpecLayerScope
			Mode      api.PermissionMode
			Presets   []api.Preset
			Tools     api.Tools
		}
		actual := make([]posture, 0, len(presets))
		for _, preset := range presets {
			Expect(preset.ID).To(Equal(EncodeID(KindPreset, BuiltinSourceID, preset.Key)))
			Expect(preset.Description).NotTo(BeEmpty(), preset.Name)
			permissions := preset.Spec.Permissions
			actual = append(actual, posture{preset.Key, preset.Name, preset.Scope, permissions.Mode, permissions.Presets, permissions.Tools})
		}
		Expect(actual).To(Equal([]posture{
			{Key: "edit", Name: "Edit", Scope: api.SpecLayerContext, Mode: api.PermissionAcceptEdits, Presets: []api.Preset{api.PresetEdit}},
			{Key: "plan", Name: "Plan", Scope: api.SpecLayerContext, Mode: api.PermissionPlan, Tools: api.Tools{
				"read": api.ToolPolicyAllow, "shell": api.ToolPolicyDeny,
			}},
			{Key: "read-only", Name: "Read-only", Scope: api.SpecLayerContext, Tools: api.Tools{
				"edit": api.ToolPolicyDeny, "write": api.ToolPolicyDeny,
				"shell": api.ToolPolicyDeny, "web_search": api.ToolPolicyDeny, "web_fetch": api.ToolPolicyDeny,
			}},
		}))
	})

	It("keeps Read-only's web denies when Plan is layered after it", func(ctx SpecContext) {
		f := newBuiltinFixture()
		resolution, err := f.catalog.ResolvePresets(ctx, []string{"Read-only", "Plan"})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Resolved.Spec.Permissions.Mode).To(Equal(api.PermissionPlan))
		Expect(resolution.Resolved.Spec.Permissions.Tools).To(Equal(api.Tools{
			"read": api.ToolPolicyAllow, "edit": api.ToolPolicyDeny, "write": api.ToolPolicyDeny,
			"shell": api.ToolPolicyDeny, "web_search": api.ToolPolicyDeny, "web_fetch": api.ToolPolicyDeny,
		}))
	})

	It("gets by key and refuses every write", func(ctx SpecContext) {
		store := NewBuiltinSource().Presets()
		plan, err := store.Get(ctx, "plan")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Name).To(Equal("Plan"))
		_, err = store.Get(ctx, "missing")
		Expect(err).To(MatchError(ErrNotFound))
		_, err = store.Get(ctx, "../plan")
		Expect(err).To(MatchError(ErrNotFound))
		_, err = store.Create(ctx, globalPreset("New"))
		Expect(err).To(MatchError(ErrReadOnly))
		_, err = store.Update(ctx, "plan", globalPreset("Plan"))
		Expect(err).To(MatchError(ErrReadOnly))
		Expect(store.Delete(ctx, "plan")).To(MatchError(ErrReadOnly))
	})
})

var _ = Describe("Catalog built-in overrides", func() {
	It("lists the built-ins and resolves them by name and id when nothing overrides them", func(ctx SpecContext) {
		f := newBuiltinFixture()
		org, err := f.catalog.CreatePreset(ctx, "", globalPreset("Organization"))
		Expect(err).NotTo(HaveOccurred())

		listed, err := f.catalog.ListPresets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(presetSummaries(listed)).To(Equal([][2]string{
			{"db", org.Name}, {"builtin", "Edit"}, {"builtin", "Plan"}, {"builtin", "Read-only"},
		}))
		plan, err := f.catalog.GetPreset(ctx, "Plan")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.ID).To(Equal(builtinPlanID))
		Expect(f.catalog.GetPreset(ctx, builtinPlanID)).To(Equal(plan))
	})

	It("lets a file preset with a built-in's name shadow it in lists, lookups and layers", func(ctx SpecContext) {
		f := newBuiltinFixture()
		writeRecordFile(f.presets.Info().Root, "plan.yaml", "name: plan\nscope: user\nspec:\n  permissions:\n    mode: default\n")

		listed, err := f.catalog.ListPresets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(presetSummaries(listed)).To(Equal([][2]string{
			{"file", "plan"}, {"builtin", "Edit"}, {"builtin", "Read-only"},
		}))
		override := listed[0]
		Expect(f.catalog.GetPreset(ctx, "PLAN")).To(Equal(override))
		Expect(f.catalog.GetPreset(ctx, builtinPlanID)).To(Equal(override))

		resolution, err := f.catalog.ResolvePresets(ctx, []string{builtinPlanID})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Presets).To(Equal([]Preset{override}))
		Expect(resolution.Layers).To(HaveLen(1))
		Expect(resolution.Layers[0].ID).To(Equal(override.ID))
		Expect(resolution.Resolved.Spec.Permissions.Mode).To(Equal(api.PermissionDefault))
	})

	It("lets a database preset created with a built-in's name shadow it until it is deleted", func(ctx SpecContext) {
		f := newBuiltinFixture()
		override, err := f.catalog.CreatePreset(ctx, "", PresetInput{Name: "Plan", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred(), "a built-in name does not block a create")

		listed, err := f.catalog.ListPresets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(presetSummaries(listed)).To(Equal([][2]string{
			{"db", "Plan"}, {"builtin", "Edit"}, {"builtin", "Read-only"},
		}))
		Expect(f.catalog.GetPreset(ctx, "plan")).To(Equal(override))
		Expect(f.catalog.GetPreset(ctx, builtinPlanID)).To(Equal(override))
		layers, err := f.catalog.PresetLayers(ctx, []string{"Plan"})
		Expect(err).NotTo(HaveOccurred())
		Expect(layers.Layers[0].ID).To(Equal(override.ID))

		Expect(f.catalog.DeletePreset(ctx, override.ID)).To(Succeed(), "the override is deleted through its own id")
		restored, err := f.catalog.GetPreset(ctx, "plan")
		Expect(err).NotTo(HaveOccurred())
		Expect(restored.ID).To(Equal(builtinPlanID))
		Expect(restored.Source.Kind).To(Equal(SourceBuiltin))
	})

	It("keeps two user presets with a built-in's name ambiguous and unique", func(ctx SpecContext) {
		f := newBuiltinFixture()
		_, err := f.catalog.CreatePreset(ctx, "", PresetInput{Name: "Plan", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())
		_, err = f.catalog.CreatePreset(ctx, f.presets.Info().ID, PresetInput{Name: "plan", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrNameTaken))

		writeRecordFile(f.presets.Info().Root, "plan.yaml", "name: plan\nscope: user\n")
		_, err = f.catalog.GetPreset(ctx, "plan")
		Expect(err).To(MatchError(ErrAmbiguous))
		_, err = f.catalog.GetPreset(ctx, builtinPlanID)
		Expect(err).To(MatchError(ErrAmbiguous))
	})

	It("refuses to write or delete a built-in nothing overrides", func(ctx SpecContext) {
		f := newBuiltinFixture()
		_, err := f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "Planning", Presets: []string{"Plan"}})
		Expect(err).NotTo(HaveOccurred())

		_, err = f.catalog.UpdatePreset(ctx, "plan", PresetInput{Name: "Plan", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrReadOnly))
		_, err = f.catalog.UpdatePreset(ctx, builtinPlanID, PresetInput{Name: "Plan", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrReadOnly))
		Expect(f.catalog.DeletePreset(ctx, "plan")).To(MatchError(ErrReadOnly), "read-only wins over references")
		_, err = f.catalog.CreatePreset(ctx, BuiltinSourceID, PresetInput{Name: "Mine", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrReadOnly))
	})

	It("refuses writes addressed by a built-in id even when an override answers reads for it", func(ctx SpecContext) {
		f := newBuiltinFixture()
		writeRecordFile(f.presets.Info().Root, "plan.yaml", "name: Plan\nscope: user\nspec:\n  permissions:\n    mode: default\n")
		override, err := f.catalog.GetPreset(ctx, builtinPlanID)
		Expect(err).NotTo(HaveOccurred())
		Expect(override.Source.Kind).To(Equal(SourceFile), "reads still follow the override")

		_, err = f.catalog.UpdatePreset(ctx, builtinPlanID, PresetInput{Name: "Plan", Scope: api.SpecLayerContext})
		Expect(err).To(MatchError(ErrReadOnly))
		Expect(f.catalog.DeletePreset(ctx, builtinPlanID)).To(MatchError(ErrReadOnly))
		Expect(f.catalog.GetPreset(ctx, "plan")).To(Equal(override), "the override is untouched")

		updated, err := f.catalog.UpdatePreset(ctx, "plan", PresetInput{Name: "Plan", Scope: api.SpecLayerContext})
		Expect(err).NotTo(HaveOccurred(), "a bare name still writes the effective record")
		Expect(updated.ID).To(Equal(override.ID))
		Expect(updated.Scope).To(Equal(api.SpecLayerContext))
	})

	It("deletes an override that profiles name, but not one they reference by id", func(ctx SpecContext) {
		f := newBuiltinFixture()
		override, err := f.catalog.CreatePreset(ctx, "", PresetInput{Name: "Plan", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())
		byName, err := f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "By name", Presets: []string{"plan"}})
		Expect(err).NotTo(HaveOccurred())
		byID, err := f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "By id", Presets: []string{override.ID}})
		Expect(err).NotTo(HaveOccurred())

		err = f.catalog.DeletePreset(ctx, "plan")
		var referenced ReferencedError
		Expect(errors.As(err, &referenced)).To(BeTrue(), err)
		Expect(referenced.Profiles).To(Equal([]Profile{byID}))

		Expect(f.catalog.DeleteProfile(ctx, byID.ID)).To(Succeed())
		Expect(f.catalog.DeletePreset(ctx, "plan")).To(Succeed())
		resolution, err := f.catalog.Layers(ctx, byName.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Profile.Presets).To(Equal([]string{builtinPlanID}), "the built-in takes over the name")
	})

	It("still refuses to delete a preset without a built-in counterpart that profiles name", func(ctx SpecContext) {
		f := newBuiltinFixture()
		_, err := f.catalog.CreatePreset(ctx, "", PresetInput{Name: "Custom", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())
		_, err = f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "By name", Presets: []string{"custom"}})
		Expect(err).NotTo(HaveOccurred())
		var referenced ReferencedError
		Expect(errors.As(f.catalog.DeletePreset(ctx, "custom"), &referenced)).To(BeTrue())
	})
})
