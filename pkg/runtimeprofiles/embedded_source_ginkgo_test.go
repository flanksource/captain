package runtimeprofiles

import (
	"path/filepath"
	"testing/fstest"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const hostSourceID = "acme"

// hostPresetFS is what a host embedding captain ships: one preset of its own
// and one that reuses a built-in's name to retune it.
func hostPresetFS() fstest.MapFS {
	return fstest.MapFS{
		"presets/acme-debug.yaml": {Data: []byte("name: Acme debug\ndescription: Debug the acme service\nscope: context\nspec:\n  permissions:\n    mode: acceptEdits\n")},
		"presets/plan.yaml":       {Data: []byte("name: Plan\ndescription: Acme planning\nscope: context\nspec:\n  permissions:\n    mode: default\n")},
		"presets/README.md":       {Data: []byte("not a preset")},
		"presets/nested/x.yaml":   {Data: []byte("name: Nested\nscope: context\n")},
	}
}

func newHostSource() Source {
	GinkgoHelper()
	source, err := NewEmbeddedSource(hostPresetFS(), "presets", SourceInfo{ID: hostSourceID, Label: "Acme"})
	Expect(err).NotTo(HaveOccurred())
	return source
}

var _ = Describe("Embedded source", func() {
	It("is a read-only built-in preset source under the host's id and label", func() {
		source := newHostSource()
		Expect(source.Info()).To(Equal(SourceInfo{
			Kind: SourceBuiltin, ID: hostSourceID, Label: "Acme", Writable: false, Records: []Kind{KindPreset},
		}))
		Expect(source.Profiles()).To(BeNil())
	})

	It("lists the directory's yaml presets by name and gets them by key", func(ctx SpecContext) {
		store := newHostSource().Presets()
		listed, err := store.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		type summary struct{ ID, Key, Name, Description string }
		actual := make([]summary, 0, len(listed))
		for _, preset := range listed {
			actual = append(actual, summary{preset.ID, preset.Key, preset.Name, preset.Description})
		}
		Expect(actual).To(Equal([]summary{
			{EncodeID(KindPreset, hostSourceID, "acme-debug"), "acme-debug", "Acme debug", "Debug the acme service"},
			{EncodeID(KindPreset, hostSourceID, "plan"), "plan", "Plan", "Acme planning"},
		}))
		debug, err := store.Get(ctx, "acme-debug")
		Expect(err).NotTo(HaveOccurred())
		Expect(debug).To(Equal(listed[0]))
		Expect(debug.Spec.Permissions.Mode).To(Equal(api.PermissionAcceptEdits))
		_, err = store.Get(ctx, "missing")
		Expect(err).To(MatchError(ErrNotFound))
		_, err = store.Get(ctx, "../presets/plan")
		Expect(err).To(MatchError(ErrNotFound))
		_, err = store.Create(ctx, globalPreset("New"))
		Expect(err).To(MatchError(ErrReadOnly))
		Expect(store.Delete(ctx, "plan")).To(MatchError(ErrReadOnly))
	})

	It("rejects a yaml file whose name is not a valid preset key", func(ctx SpecContext) {
		fsys := fstest.MapFS{"presets/Bad Name.yaml": {Data: []byte("name: Bad\nscope: context\n")}}
		source, err := NewEmbeddedSource(fsys, "presets", SourceInfo{ID: hostSourceID, Label: "Acme"})
		Expect(err).NotTo(HaveOccurred())
		_, err = source.Presets().List(ctx)
		Expect(err).To(MatchError(ErrInvalid))
		Expect(err).To(MatchError(ContainSubstring(`"Bad Name.yaml"`)))
	})

	DescribeTable("refuses an unusable directory or source description",
		func(dir string, info SourceInfo, message string) {
			_, err := NewEmbeddedSource(hostPresetFS(), dir, info)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("missing directory", "absent", SourceInfo{ID: hostSourceID, Label: "Acme"}, "absent"),
		Entry("a file, not a directory", "presets/plan.yaml", SourceInfo{ID: hostSourceID, Label: "Acme"}, "not a directory"),
		Entry("no id", "presets", SourceInfo{Label: "Acme"}, "requires an id"),
		Entry("no label", "presets", SourceInfo{ID: hostSourceID}, "requires a label"),
		Entry("another kind", "presets", SourceInfo{ID: hostSourceID, Label: "Acme", Kind: SourceFile}, `kind "file"`),
		Entry("writable", "presets", SourceInfo{ID: hostSourceID, Label: "Acme", Writable: true}, "read-only"),
		Entry("profiles", "presets", SourceInfo{ID: hostSourceID, Label: "Acme", Records: []Kind{KindProfile}}, "holds only presets"),
	)
})

var _ = Describe("Catalog host source precedence", func() {
	It("lets a host source registered before the built-ins override a built-in of the same name", func(ctx SpecContext) {
		catalog, err := NewCatalog(newHostSource(), NewBuiltinSource())
		Expect(err).NotTo(HaveOccurred())
		listed, err := catalog.ListPresets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(presetSources(listed)).To(Equal([][2]string{
			{hostSourceID, "Acme debug"}, {hostSourceID, "Plan"}, {BuiltinSourceID, "Edit"}, {BuiltinSourceID, "Read-only"},
		}))
		plan, err := catalog.GetPreset(ctx, "plan")
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Description).To(Equal("Acme planning"))
		Expect(catalog.GetPreset(ctx, builtinPlanID)).To(Equal(plan), "a stored built-in id follows the name to the host preset")
		resolution, err := catalog.ResolvePresets(ctx, []string{"Plan"})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Resolved.Spec.Permissions.Mode).To(Equal(api.PermissionDefault))
	})

	It("lets a repo-dir preset override a host preset through the default catalog", func(ctx SpecContext) {
		home, cwd := GinkgoT().TempDir(), GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		GinkgoT().Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
		writeRecordFile(filepath.Join(cwd, ".captain", "presets"), "acme-debug.yaml",
			"name: Acme debug\ndescription: Repo override\nscope: context\nspec:\n  permissions:\n    mode: plan\n")
		host := newHostSource()
		catalog, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd, Config: &captainconfig.Config{}, Sources: []Source{host}})
		Expect(err).NotTo(HaveOccurred())
		sources := catalog.Sources()
		Expect(sources[len(sources)-2:]).To(Equal([]SourceInfo{host.Info(), NewBuiltinSource().Info()}),
			"host sources sit between the file directories and the built-ins")

		listed, err := catalog.ListPresets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(presetSummaries(listed)).To(Equal([][2]string{
			{"file", "Acme debug"}, {"builtin", "Plan"}, {"builtin", "Edit"}, {"builtin", "Read-only"},
		}))
		debug, err := catalog.GetPreset(ctx, "ACME debug")
		Expect(err).NotTo(HaveOccurred())
		Expect(debug.Description).To(Equal("Repo override"))
		Expect(catalog.GetPreset(ctx, EncodeID(KindPreset, hostSourceID, "acme-debug"))).To(Equal(debug))
	})

	It("refuses a host source that is not an embedded preset source", func(ctx SpecContext) {
		_, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: GinkgoT().TempDir(), Config: &captainconfig.Config{},
			Sources: []Source{newMemSource("mem", SourceDB, true)}})
		Expect(err).To(MatchError(ContainSubstring("host runtime source")))
		_, err = NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: GinkgoT().TempDir(), Config: &captainconfig.Config{},
			Sources: []Source{nil}})
		Expect(err).To(MatchError(ContainSubstring("host runtime source 0 is nil")))
	})
})

func presetSources(presets []Preset) [][2]string {
	out := make([][2]string, 0, len(presets))
	for _, preset := range presets {
		out = append(out, [2]string{preset.Source.ID, preset.Name})
	}
	return out
}
