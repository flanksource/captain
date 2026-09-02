package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/clicky/entity"
	clickyrpc "github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type runtimeEntityFixture struct {
	ctx      context.Context
	catalog  *runtimeprofiles.Catalog
	presets  runtimeprofiles.SourceInfo
	profiles runtimeprofiles.SourceInfo
}

// newRuntimeEntityFixture builds a catalog from one preset directory and one
// profile directory (plus any leading sources) and pins it on the context the
// entity handlers read.
func newRuntimeEntityFixture(leading ...runtimeprofiles.Source) runtimeEntityFixture {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	presets, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
		Kind: runtimeprofiles.KindPreset, Dir: filepath.Join(root, "presets"), Label: "team presets", Implicit: true,
	})
	Expect(err).NotTo(HaveOccurred())
	profiles, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
		Kind: runtimeprofiles.KindProfile, Dir: filepath.Join(root, "profiles"), Label: "team profiles", Implicit: true,
	})
	Expect(err).NotTo(HaveOccurred())
	catalog, err := runtimeprofiles.NewCatalog(append(leading, presets, profiles)...)
	Expect(err).NotTo(HaveOccurred())
	return runtimeEntityFixture{
		ctx:     ContextWithRuntimeCatalog(context.Background(), catalog),
		catalog: catalog, presets: presets.Info(), profiles: profiles.Info(),
	}
}

// httpBody stages a JSON body the way the HTTP executor does, so a handler
// reads the nested document instead of the flattened key=value map.
func (f runtimeEntityFixture) httpBody(body any) context.Context {
	GinkgoHelper()
	data, err := json.Marshal(body)
	Expect(err).NotTo(HaveOccurred())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime-preset", bytes.NewReader(data))
	return clickyrpc.ContextWithRequest(f.ctx, request)
}

func (f runtimeEntityFixture) createPreset(body map[string]any) RuntimePresetRecord {
	GinkgoHelper()
	record, err := createRuntimePreset(f.httpBody(body), nil)
	Expect(err).NotTo(HaveOccurred())
	return record
}

func (f runtimeEntityFixture) createProfile(body map[string]any) RuntimeProfileRecord {
	GinkgoHelper()
	record, err := createRuntimeProfile(f.httpBody(body), nil)
	Expect(err).NotTo(HaveOccurred())
	return record
}

func statusOf(err error) int {
	GinkgoHelper()
	var status *entity.StatusError
	Expect(errors.As(err, &status)).To(BeTrue(), "expected a status error, got %v", err)
	return status.StatusCode()
}

func jsonKeys(value any) []string {
	GinkgoHelper()
	data, err := json.Marshal(value)
	Expect(err).NotTo(HaveOccurred())
	var object map[string]json.RawMessage
	Expect(json.Unmarshal(data, &object)).To(Succeed())
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}

func printJSON(label string, value any) {
	GinkgoHelper()
	data, err := json.MarshalIndent(value, "", "  ")
	Expect(err).NotTo(HaveOccurred())
	GinkgoWriter.Printf("%s:\n%s\n", label, data)
}

var organizationPresetBody = map[string]any{
	"name": "Organization", "scope": "global", "description": "Shared defaults",
	"spec": map[string]any{"model": "claude-sonnet-4-6", "mode": "cli", "budget": map[string]any{"maxTurns": 20}},
}

var personalPresetBody = map[string]any{
	"name": "Personal", "scope": "user",
	"spec": map[string]any{"permissions": map[string]any{"tools": map[string]any{"Bash": "allow"}}},
}

func withTarget(body map[string]any, target string) map[string]any {
	out := map[string]any{"target": target}
	for key, value := range body {
		out[key] = value
	}
	return out
}

var _ = Describe("runtime entities over file sources", func() {
	var f runtimeEntityFixture

	BeforeEach(func() {
		f = newRuntimeEntityFixture()
	})

	It("creates a preset in the named file source and returns the stored record", func() {
		record := f.createPreset(withTarget(organizationPresetBody, f.presets.ID))

		Expect(filepath.Join(f.presets.Root, "organization.yaml")).To(BeAnExistingFile())
		Expect(record.ID).To(Equal(runtimeprofiles.EncodeID(runtimeprofiles.KindPreset, f.presets.ID, "organization")))
		Expect(record.Source.Kind).To(Equal(runtimeprofiles.SourceFile))
		Expect(record.Spec.Budget.MaxTurns).To(Equal(20))
		Expect(jsonKeys(record)).To(ConsistOf("id", "key", "source", "name", "description", "scope", "spec", "updatedAt"))
		Expect(jsonKeys(record.Source)).To(ConsistOf("kind", "id", "label", "root", "writable", "implicit", "records"))
		printJSON("preset list item", record)
	})

	It("lists whole records sorted by source kind then name and honours the filters", func() {
		f.createPreset(withTarget(map[string]any{"name": "beta", "scope": "user"}, f.presets.ID))
		f.createPreset(withTarget(map[string]any{"name": "Alpha", "scope": "global", "description": "first"}, f.presets.ID))

		listed, err := listRuntimePresets(f.ctx, RuntimePresetListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(2))
		Expect([]string{listed[0].Name, listed[1].Name}).To(Equal([]string{"Alpha", "beta"}))
		Expect(jsonKeys(listed[0])).To(ContainElements("spec", "source"))

		Expect(listRuntimePresets(f.ctx, RuntimePresetListOptions{Query: "FIRST"})).To(HaveLen(1))
		Expect(listRuntimePresets(f.ctx, RuntimePresetListOptions{Source: "file"})).To(HaveLen(2))
		Expect(listRuntimePresets(f.ctx, RuntimePresetListOptions{Source: f.presets.ID})).To(HaveLen(2))
		Expect(listRuntimePresets(f.ctx, RuntimePresetListOptions{Source: "db"})).To(BeEmpty())
		Expect(listRuntimePresets(f.ctx, RuntimePresetListOptions{Scope: "user"})).To(HaveLen(1))
		_, err = listRuntimePresets(f.ctx, RuntimePresetListOptions{Scope: "team"})
		Expect(statusOf(err)).To(Equal(http.StatusBadRequest))
	})

	It("gets a preset by unique name or encoded id", func() {
		created := f.createPreset(withTarget(organizationPresetBody, f.presets.ID))

		Expect(getRuntimePreset(f.ctx, "organization")).To(Equal(created))
		Expect(getRuntimePreset(f.ctx, created.ID)).To(Equal(created))
		_, err := getRuntimePreset(f.ctx, "missing")
		Expect(statusOf(err)).To(Equal(http.StatusNotFound))
	})

	It("renames on update, keeps the key and ignores a target", func() {
		created := f.createPreset(withTarget(organizationPresetBody, f.presets.ID))

		renamed, err := updateRuntimePreset(f.httpBody(map[string]any{
			"id": created.ID, "target": "elsewhere", "name": "Org", "scope": "global",
		}), created.ID, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(renamed.Name).To(Equal("Org"))
		Expect(renamed.Key).To(Equal("organization"))
		Expect(renamed.Source).To(Equal(f.presets))
		_, err = getRuntimePreset(f.ctx, "organization")
		Expect(statusOf(err)).To(Equal(http.StatusNotFound))

		_, err = updateRuntimePreset(f.httpBody(map[string]any{"id": "someone-else", "name": "Org", "scope": "global"}), created.ID, nil)
		Expect(statusOf(err)).To(Equal(http.StatusBadRequest), "a body id must be the routed id")
	})

	It("refuses to delete a preset a profile references with 409", func() {
		f.createPreset(withTarget(organizationPresetBody, f.presets.ID))
		review := f.createProfile(withTarget(map[string]any{"name": "Review", "presets": []string{"organization"}}, f.profiles.ID))

		err := deleteRuntimePreset(f.ctx, "organization")
		Expect(statusOf(err)).To(Equal(http.StatusConflict))
		Expect(err.Error()).To(ContainSubstring("Review"))
		Expect(filepath.Join(f.presets.Root, "organization.yaml")).To(BeAnExistingFile())

		Expect(deleteRuntimeProfile(f.ctx, review.ID)).To(Succeed())
		Expect(deleteRuntimePreset(f.ctx, "organization")).To(Succeed())
		Expect(filepath.Join(f.presets.Root, "organization.yaml")).NotTo(BeAnExistingFile())
	})

	DescribeTable("rejects a malformed write body with 400",
		func(body map[string]any) {
			_, err := createRuntimePreset(f.httpBody(withTarget(body, f.presets.ID)), nil)
			Expect(statusOf(err)).To(Equal(http.StatusBadRequest))
		},
		Entry("content alongside structured fields", map[string]any{"name": "Both", "content": "name: Both\nscope: user\n"}),
		Entry("unknown field", map[string]any{"name": "Typo", "scope": "user", "colour": "red"}),
		Entry("unexpanded @file content", map[string]any{"content": "@preset.yaml"}),
		Entry("unknown key inside content", map[string]any{"content": "name: Typo\nscope: user\ncolour: red\n"}),
		Entry("unknown target", map[string]any{"name": "Lost", "scope": "user", "target": "nowhere"}),
		Entry("caller-chosen id", map[string]any{"name": "Chosen", "scope": "user", "id": "mine"}),
	)

	It("creates from YAML content on the CLI path", func() {
		record, err := createRuntimePreset(f.ctx, map[string]any{
			"target":  f.presets.ID,
			"content": "name: From file\nscope: user\nspec:\n  budget:\n    maxTurns: 3\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(record.Key).To(Equal("from-file"))
		Expect(record.Spec.Budget.MaxTurns).To(Equal(3))
	})

	It("resolves a profile through its presets in reference order", func() {
		organization := f.createPreset(withTarget(organizationPresetBody, f.presets.ID))
		personal := f.createPreset(withTarget(personalPresetBody, f.presets.ID))
		f.createProfile(withTarget(map[string]any{
			"name": "Review", "presets": []string{"personal", organization.ID},
			"spec": map[string]any{"budget": map[string]any{"maxTurns": 5}},
		}, f.profiles.ID))

		resolution, err := resolveRuntimeProfileAction(f.ctx, "review", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Profile.Presets).To(Equal([]string{personal.ID, organization.ID}))
		Expect(resolution.Presets).To(Equal([]runtimeprofiles.Preset{personal.Preset, organization.Preset}))
		names := make([]string, 0, len(resolution.Resolved.Trace))
		for _, layer := range resolution.Resolved.Trace {
			names = append(names, layer.Name)
		}
		Expect(names).To(Equal([]string{"Organization", "Review run spec", "Personal"}))
		Expect(resolution.Resolved.Spec.Budget.MaxTurns).To(Equal(5))
		Expect(resolution.Resolved.Spec.Model.Mode).To(Equal(api.ModeCLI))
		Expect(jsonKeys(resolution)).To(ConsistOf("profile", "presets", "resolved"))
		printJSON("resolve response", resolution)

		_, err = resolveRuntimeProfileAction(f.ctx, "missing", nil)
		Expect(statusOf(err)).To(Equal(http.StatusNotFound))
	})

	It("lists only the profiles referencing a preset when asked", func() {
		organization := f.createPreset(withTarget(organizationPresetBody, f.presets.ID))
		f.createProfile(withTarget(map[string]any{"name": "By name", "presets": []string{"organization"}}, f.profiles.ID))
		f.createProfile(withTarget(map[string]any{"name": "By id", "presets": []string{organization.ID}}, f.profiles.ID))
		f.createProfile(withTarget(map[string]any{"name": "Unrelated"}, f.profiles.ID))

		listed, err := listRuntimeProfiles(f.ctx, RuntimeProfileListOptions{Preset: "organization"})
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{listed[0].Name, listed[1].Name}).To(Equal([]string{"By id", "By name"}))
		Expect(jsonKeys(listed[0])).To(ConsistOf("id", "key", "source", "name", "spec", "presets", "updatedAt"))

		Expect(listRuntimeProfiles(f.ctx, RuntimeProfileListOptions{})).To(HaveLen(3))
		_, err = listRuntimeProfiles(f.ctx, RuntimeProfileListOptions{Preset: "missing"})
		Expect(statusOf(err)).To(Equal(http.StatusNotFound))
	})

	It("serves the catalog sources in the prompt schema document", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(captainconfig.SetPathForTesting, "")
		previous := schemaAdapters
		DeferCleanup(func() { schemaAdapters = previous })
		schemaAdapters = func() ([]AdapterStatus, error) {
			return ai.ProbeAdapters(ai.WhoamiOptions{Models: true}, fakeSchemaProbe())
		}

		doc, err := PromptSchemaDocument(f.ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(doc["runtimeSources"]).To(Equal([]runtimeprofiles.SourceInfo{f.presets, f.profiles}))
	})
})
