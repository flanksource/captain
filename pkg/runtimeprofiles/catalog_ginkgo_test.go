package runtimeprofiles

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// memStore is the in-memory stand-in for a database source: keys are
// sequential, and it applies the same input contract the real stores do.
type memStore[R record, I input[R, I]] struct {
	info    SourceInfo
	kind    Kind
	records map[string]R
	next    int
}

func (m *memStore[R, I]) List(context.Context) ([]R, error) {
	keys := slices.Sorted(maps.Keys(m.records))
	out := make([]R, 0, len(keys))
	for _, key := range keys {
		out = append(out, m.records[key])
	}
	return out, nil
}

func (m *memStore[R, I]) Get(_ context.Context, key string) (R, error) {
	record, ok := m.records[key]
	if !ok {
		var zero R
		return zero, fmt.Errorf("%w: %s %q", ErrNotFound, m.kind, key)
	}
	return record, nil
}

func (m *memStore[R, I]) put(key string, in I) R {
	record := in.trimmed().build(recordMeta{
		ID: EncodeID(m.kind, m.info.ID, key), Key: key, Source: m.info, UpdatedAt: time.Now(),
	})
	m.records[key] = record
	return record
}

func (m *memStore[R, I]) Create(_ context.Context, in I) (R, error) {
	m.next++
	return m.put(fmt.Sprintf("%s-%d", m.kind, m.next), in), nil
}

func (m *memStore[R, I]) Update(ctx context.Context, key string, in I) (R, error) {
	if _, err := m.Get(ctx, key); err != nil {
		var zero R
		return zero, err
	}
	return m.put(key, in), nil
}

func (m *memStore[R, I]) Delete(ctx context.Context, key string) error {
	if _, err := m.Get(ctx, key); err != nil {
		return err
	}
	delete(m.records, key)
	return nil
}

type memSource struct {
	info     SourceInfo
	presets  *memStore[Preset, PresetInput]
	profiles *memStore[Profile, ProfileInput]
}

func newMemSource(id string, kind SourceKind, writable bool) *memSource {
	info := SourceInfo{Kind: kind, ID: id, Label: "memory " + id, Writable: writable, Records: []Kind{KindPreset, KindProfile}}
	return &memSource{
		info:     info,
		presets:  &memStore[Preset, PresetInput]{info: info, kind: KindPreset, records: map[string]Preset{}},
		profiles: &memStore[Profile, ProfileInput]{info: info, kind: KindProfile, records: map[string]Profile{}},
	}
}

func (s *memSource) Info() SourceInfo                       { return s.info }
func (s *memSource) Presets() Store[Preset, PresetInput]    { return s.presets }
func (s *memSource) Profiles() Store[Profile, ProfileInput] { return s.profiles }

type catalogFixture struct {
	catalog  *Catalog
	db       *memSource
	presets  Source
	profiles Source
}

func newCatalogFixture() catalogFixture {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	presets, err := NewFileSource(FileSourceOptions{Kind: KindPreset, Dir: filepath.Join(root, "presets"), Label: "team presets", Implicit: true})
	Expect(err).NotTo(HaveOccurred())
	profiles, err := NewFileSource(FileSourceOptions{Kind: KindProfile, Dir: filepath.Join(root, "profiles"), Label: "team profiles", Implicit: true})
	Expect(err).NotTo(HaveOccurred())
	db := newMemSource("db", SourceDB, true)
	catalog, err := NewCatalog(db, presets, profiles)
	Expect(err).NotTo(HaveOccurred())
	return catalogFixture{catalog: catalog, db: db, presets: presets, profiles: profiles}
}

func globalPreset(name string) PresetInput {
	return PresetInput{Name: name, Scope: api.SpecLayerGlobal, Spec: api.RuntimePresetSpec{
		Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent},
	}}
}

var _ = Describe("Catalog", func() {
	It("requires uniquely identified sources", func() {
		_, err := NewCatalog()
		Expect(err).To(MatchError(ContainSubstring("at least one source")))
		_, err = NewCatalog(newMemSource("db", SourceDB, true), newMemSource("db", SourceDB, true))
		Expect(err).To(MatchError(ContainSubstring(`registered twice`)))
		_, err = NewCatalog(newMemSource("", SourceDB, true))
		Expect(err).To(MatchError(ContainSubstring("has no id")))
	})

	It("lists sources in registration order", func() {
		f := newCatalogFixture()
		infos := f.catalog.Sources()
		Expect(infos).To(HaveLen(3))
		Expect(infos[0]).To(Equal(f.db.info))
		Expect(infos[1].Label).To(Equal("team presets"))
		Expect(infos[2].Label).To(Equal("team profiles"))
	})

	It("routes creates to the database by default and to a named source otherwise", func(ctx SpecContext) {
		f := newCatalogFixture()
		inDB, err := f.catalog.CreatePreset(ctx, "", globalPreset("Organization"))
		Expect(err).NotTo(HaveOccurred())
		Expect(inDB.Source.ID).To(Equal("db"))
		Expect(inDB.ID).To(Equal(EncodeID(KindPreset, "db", inDB.Key)))

		inFile, err := f.catalog.CreatePreset(ctx, f.presets.Info().ID, PresetInput{Name: "Personal", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())
		Expect(inFile.Source.Label).To(Equal("team presets"))
		Expect(inFile.Key).To(Equal("personal"))

		listed, err := f.catalog.ListPresets(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(Equal([]Preset{inDB, inFile}), "sources are listed in registration order")

		_, err = f.catalog.CreatePreset(ctx, "nope", globalPreset("Elsewhere"))
		Expect(err).To(MatchError(ContainSubstring(`unknown runtime source "nope"`)))
		_, err = f.catalog.CreatePreset(ctx, f.profiles.Info().ID, globalPreset("Wrong kind"))
		Expect(err).To(MatchError(ContainSubstring("holds no presets")))
		_, err = f.catalog.CreateProfile(ctx, f.presets.Info().ID, ProfileInput{Name: "Wrong kind"})
		Expect(err).To(MatchError(ContainSubstring("holds no profiles")))
		_, err = f.catalog.CreatePreset(ctx, "", PresetInput{Name: "", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrInvalid))
	})

	It("needs a database source for the default target", func(ctx SpecContext) {
		source, err := NewFileSource(FileSourceOptions{Kind: KindPreset, Dir: GinkgoT().TempDir()})
		Expect(err).NotTo(HaveOccurred())
		catalog, err := NewCatalog(source)
		Expect(err).NotTo(HaveOccurred())
		_, err = catalog.CreatePreset(ctx, "", globalPreset("Nowhere"))
		Expect(err).To(MatchError(ContainSubstring("no database source")))
		created, err := catalog.CreatePreset(ctx, source.Info().ID, globalPreset("Somewhere"))
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Source.ID).To(Equal(source.Info().ID))
	})

	It("resolves bare names case-insensitively across sources and ids exactly", func(ctx SpecContext) {
		f := newCatalogFixture()
		org, err := f.catalog.CreatePreset(ctx, "", globalPreset("Organization"))
		Expect(err).NotTo(HaveOccurred())
		personal, err := f.catalog.CreatePreset(ctx, f.presets.Info().ID, PresetInput{Name: "Personal", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())

		Expect(f.catalog.GetPreset(ctx, "ORGANIZATION")).To(Equal(org))
		Expect(f.catalog.GetPreset(ctx, "personal")).To(Equal(personal))
		Expect(f.catalog.GetPreset(ctx, personal.ID)).To(Equal(personal))
		Expect(f.catalog.GetPreset(ctx, " "+org.ID+" ")).To(Equal(org))

		_, err = f.catalog.GetPreset(ctx, "missing")
		Expect(err).To(MatchError(ErrNotFound))
		_, err = f.catalog.GetPreset(ctx, "")
		Expect(err).To(MatchError(ErrNotFound))
		_, err = f.catalog.GetPreset(ctx, EncodeID(KindPreset, "db", "preset-99"))
		Expect(err).To(MatchError(ErrNotFound))
		_, err = f.catalog.GetPreset(ctx, EncodeID(KindPreset, "elsewhere", "x"))
		Expect(err).To(MatchError(ErrNotFound))
		_, err = f.catalog.GetPreset(ctx, EncodeID(KindProfile, "db", "x"))
		Expect(err).To(MatchError(ErrNotFound))
		Expect(err).To(MatchError(ContainSubstring("is a profile id, not a preset")))
		_, err = f.catalog.GetPreset(ctx, EncodeID(KindPreset, f.profiles.Info().ID, "x"))
		Expect(err).To(MatchError(ContainSubstring("holds no presets")))
	})

	It("reports a name that matches records in several sources as ambiguous", func(ctx SpecContext) {
		f := newCatalogFixture()
		f.db.presets.put("preset-1", PresetInput{Name: "Shared", Scope: api.SpecLayerUser})
		writeRecordFile(f.presets.Info().Root, "shared.yaml", "name: shared\nscope: user\n")

		_, err := f.catalog.GetPreset(ctx, "shared")
		Expect(err).To(MatchError(ErrAmbiguous))
		Expect(err).To(MatchError(ContainSubstring("memory db:preset-1")))
		Expect(err).To(MatchError(ContainSubstring("team presets:shared")))

		_, err = f.catalog.CreatePreset(ctx, "", PresetInput{Name: "SHARED", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrNameTaken))
	})

	It("keeps names unique across sources on create and rename", func(ctx SpecContext) {
		f := newCatalogFixture()
		org, err := f.catalog.CreatePreset(ctx, "", globalPreset("Organization"))
		Expect(err).NotTo(HaveOccurred())
		personal, err := f.catalog.CreatePreset(ctx, f.presets.Info().ID, PresetInput{Name: "Personal", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())

		_, err = f.catalog.CreatePreset(ctx, f.presets.Info().ID, globalPreset("organization"))
		Expect(err).To(MatchError(ErrNameTaken))
		Expect(err).To(MatchError(ContainSubstring("memory db")))

		_, err = f.catalog.UpdatePreset(ctx, personal.ID, PresetInput{Name: "ORGANIZATION", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrNameTaken))

		recased, err := f.catalog.UpdatePreset(ctx, org.ID, PresetInput{Name: "organization", Scope: api.SpecLayerGlobal})
		Expect(err).NotTo(HaveOccurred(), "a record may recase its own name")
		Expect(recased.Name).To(Equal("organization"))
		Expect(recased.ID).To(Equal(org.ID))

		renamed, err := f.catalog.UpdatePreset(ctx, "personal", PresetInput{Name: "Mine", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())
		Expect(renamed.Key).To(Equal("personal"), "a file record keeps its key")
		Expect(renamed.Name).To(Equal("Mine"))
		_, err = f.catalog.GetPreset(ctx, "personal")
		Expect(err).To(MatchError(ErrNotFound))

		_, err = f.catalog.UpdatePreset(ctx, "missing", globalPreset("Missing"))
		Expect(err).To(MatchError(ErrNotFound))
		_, err = f.catalog.UpdatePreset(ctx, org.ID, PresetInput{Name: "Bad", Scope: "team"})
		Expect(err).To(MatchError(ErrInvalid))
	})

	It("refuses writes to a read-only source", func(ctx SpecContext) {
		readOnly := newMemSource("ro", SourceDB, false)
		frozen := readOnly.presets.put("preset-1", globalPreset("Frozen"))
		catalog, err := NewCatalog(readOnly)
		Expect(err).NotTo(HaveOccurred())

		_, err = catalog.CreatePreset(ctx, "", globalPreset("New"))
		Expect(err).To(MatchError(ErrReadOnly))
		_, err = catalog.UpdatePreset(ctx, frozen.ID, globalPreset("Thawed"))
		Expect(err).To(MatchError(ErrReadOnly))
		Expect(catalog.DeletePreset(ctx, "frozen")).To(MatchError(ErrReadOnly))
		Expect(catalog.GetPreset(ctx, "frozen")).To(Equal(frozen))
	})

	It("manages profiles across sources", func(ctx SpecContext) {
		f := newCatalogFixture()
		review, err := f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "Review", Presets: []string{"Organization"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(review.Source.ID).To(Equal("db"))
		plan, err := f.catalog.CreateProfile(ctx, f.profiles.Info().ID, ProfileInput{Name: "Plan"})
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Key).To(Equal("plan"))

		_, err = f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "plan"})
		Expect(err).To(MatchError(ErrNameTaken))

		Expect(f.catalog.GetProfile(ctx, "REVIEW")).To(Equal(review))
		Expect(f.catalog.GetProfile(ctx, plan.ID)).To(Equal(plan))
		listed, err := f.catalog.ListProfiles(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(Equal([]Profile{review, plan}))

		updated, err := f.catalog.UpdateProfile(ctx, "plan", ProfileInput{Name: "Plan", Presets: []string{review.ID}})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Presets).To(Equal([]string{review.ID}))

		Expect(f.catalog.DeleteProfile(ctx, "review")).To(Succeed())
		_, err = f.catalog.GetProfile(ctx, "review")
		Expect(err).To(MatchError(ErrNotFound))
		Expect(f.catalog.DeleteProfile(ctx, "review")).To(MatchError(ErrNotFound))
	})

	It("refuses to delete a preset that profiles reference by id or by name", func(ctx SpecContext) {
		f := newCatalogFixture()
		org, err := f.catalog.CreatePreset(ctx, "", globalPreset("Organization"))
		Expect(err).NotTo(HaveOccurred())
		unused, err := f.catalog.CreatePreset(ctx, "", PresetInput{Name: "Unused", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())
		byName, err := f.catalog.CreateProfile(ctx, f.profiles.Info().ID, ProfileInput{Name: "By name", Presets: []string{"organization"}})
		Expect(err).NotTo(HaveOccurred())
		byID, err := f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "By id", Presets: []string{org.ID}})
		Expect(err).NotTo(HaveOccurred())
		_, err = f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "Unrelated"})
		Expect(err).NotTo(HaveOccurred())

		Expect(f.catalog.ReferencedBy(ctx, org)).To(Equal([]Profile{byID, byName}))
		Expect(f.catalog.ReferencedBy(ctx, unused)).To(BeEmpty())

		err = f.catalog.DeletePreset(ctx, "organization")
		var referenced ReferencedError
		Expect(errors.As(err, &referenced)).To(BeTrue(), err)
		Expect(referenced.Preset.ID).To(Equal(org.ID))
		Expect(referenced.Profiles).To(Equal([]Profile{byID, byName}))
		Expect(err.Error()).To(Equal(`runtime preset "Organization" is used by By id, By name`))
		Expect(f.catalog.GetPreset(ctx, org.ID)).To(Equal(org))

		Expect(f.catalog.DeletePreset(ctx, unused.ID)).To(Succeed())
		_, err = f.catalog.GetPreset(ctx, unused.ID)
		Expect(err).To(MatchError(ErrNotFound))
		Expect(f.catalog.DeletePreset(ctx, "unused")).To(MatchError(ErrNotFound))

		_, err = f.catalog.UpdateProfile(ctx, byID.ID, ProfileInput{Name: "By id"})
		Expect(err).NotTo(HaveOccurred())
		Expect(f.catalog.DeleteProfile(ctx, byName.ID)).To(Succeed())
		Expect(f.catalog.DeletePreset(ctx, org.ID)).To(Succeed())
	})

	It("resolves a profile through its presets in scope order with canonical ids", func(ctx SpecContext) {
		f := newCatalogFixture()
		personal, err := f.catalog.CreatePreset(ctx, f.presets.Info().ID, PresetInput{
			Name: "Personal", Scope: api.SpecLayerUser,
			Spec: api.RuntimePresetSpec{Permissions: api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyAllow}}},
		})
		Expect(err).NotTo(HaveOccurred())
		org, err := f.catalog.CreatePreset(ctx, "", PresetInput{
			Name: "Organization", Scope: api.SpecLayerGlobal,
			Spec: api.RuntimePresetSpec{
				Model:  api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeCLI},
				Budget: api.Budget{MaxTurns: 20},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = f.catalog.CreateProfile(ctx, "", ProfileInput{
			Name: "Review", Presets: []string{"personal", org.ID},
			Spec: api.Spec{Budget: api.Budget{MaxTurns: 5}},
		})
		Expect(err).NotTo(HaveOccurred())

		resolution, err := f.catalog.Resolve(ctx, "review")
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Profile.Name).To(Equal("Review"))
		Expect(resolution.Profile.Presets).To(Equal([]string{personal.ID, org.ID}), "references are canonicalised to ids")
		Expect(resolution.Presets).To(Equal([]Preset{personal, org}), "presets keep the profile's reference order")

		names := make([]string, 0, len(resolution.Resolved.Trace))
		scopes := make([]api.SpecLayerScope, 0, len(resolution.Resolved.Trace))
		for _, layer := range resolution.Resolved.Trace {
			names = append(names, layer.Name)
			scopes = append(scopes, layer.Scope)
		}
		Expect(names).To(Equal([]string{"Organization", "Review run spec", "Personal"}))
		Expect(scopes).To(Equal([]api.SpecLayerScope{api.SpecLayerGlobal, api.SpecLayerSurface, api.SpecLayerUser}))
		Expect(resolution.Resolved.Trace[0].ID).To(Equal(org.ID))
		Expect(resolution.Resolved.Spec.Model.Name).To(Equal("claude-sonnet-4-6"))
		Expect(resolution.Resolved.Spec.Model.Mode).To(Equal(api.ModeCLI))
		Expect(resolution.Resolved.Spec.Budget.MaxTurns).To(Equal(5), "the profile spec overrides the global preset")
		Expect(resolution.Resolved.Spec.Permissions.Tools).To(Equal(api.Tools{"Bash": api.ToolPolicyAllow}))
	})

	It("fails a resolution whose reference resolves nowhere, naming the profile and the reference", func(ctx SpecContext) {
		f := newCatalogFixture()
		_, err := f.catalog.CreateProfile(ctx, "", ProfileInput{Name: "Dangling", Presets: []string{"gone"}})
		Expect(err).NotTo(HaveOccurred())
		_, err = f.catalog.Resolve(ctx, "dangling")
		Expect(err).To(MatchError(ErrNotFound))
		Expect(err).To(MatchError(ContainSubstring(`runtime profile "Dangling" references preset "gone"`)))
		_, err = f.catalog.Resolve(ctx, "missing")
		Expect(err).To(MatchError(ErrNotFound))
	})
})
