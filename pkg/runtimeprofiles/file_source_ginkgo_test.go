package runtimeprofiles

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func presetSpecFixture() api.RuntimePresetSpec {
	return api.RuntimePresetSpec{
		Model: api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeCLI},
		Permissions: api.Permissions{
			Mode:  api.PermissionPlan,
			Tools: api.Tools{"Bash": api.ToolPolicyAllow, "Write": api.ToolPolicyDeny},
			MCP:   api.MCP{Servers: []string{"github"}, Modes: api.ResourcePolicies{"jira": api.ResourceDisabled}},
		},
	}
}

func presetDir(dir string, implicit bool) Source {
	GinkgoHelper()
	source, err := NewFileSource(FileSourceOptions{Kind: KindPreset, Dir: dir, Implicit: implicit})
	Expect(err).NotTo(HaveOccurred())
	return source
}

func writeRecordFile(dir, name, content string) {
	GinkgoHelper()
	Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)).To(Succeed())
}

var _ = Describe("File source", func() {
	It("requires an absolute directory and refuses a missing configured directory", func() {
		_, err := NewFileSource(FileSourceOptions{Kind: KindPreset, Dir: "relative/presets"})
		Expect(err).To(MatchError(ContainSubstring("must be absolute")))
		_, err = NewFileSource(FileSourceOptions{Kind: KindPreset, Dir: filepath.Join(GinkgoT().TempDir(), "missing")})
		Expect(err).To(MatchError(ContainSubstring("missing")))
		_, err = NewFileSource(FileSourceOptions{Kind: Kind("prompt"), Dir: GinkgoT().TempDir()})
		Expect(err).To(MatchError(ContainSubstring(`unknown record kind "prompt"`)))
	})

	It("describes itself and holds only its own kind", func(ctx SpecContext) {
		dir := filepath.Join(GinkgoT().TempDir(), "presets")
		source := presetDir(dir, true)
		Expect(source.Info()).To(Equal(SourceInfo{
			Kind: SourceFile, ID: hashDir(dir), Label: dir, Root: dir, Writable: true, Implicit: true,
			Records: []Kind{KindPreset},
		}))
		Expect(source.Profiles()).To(BeNil())
		Expect(source.Presets()).NotTo(BeNil())

		listed, err := source.Presets().List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(BeEmpty(), "an implicit directory that does not exist lists as empty")
		Expect(listed).NotTo(BeNil())
		_, err = os.Stat(dir)
		Expect(err).To(MatchError(os.ErrNotExist), "listing must not create the directory")
	})

	It("creates, reads, lists, updates and deletes preset files", func(ctx SpecContext) {
		dir := filepath.Join(GinkgoT().TempDir(), "presets")
		store := presetDir(dir, true).Presets()

		created, err := store.Create(ctx, PresetInput{
			Name: "  Personal Guardrails ", Description: " mine ", Scope: api.SpecLayerUser, Spec: presetSpecFixture(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Key).To(Equal("personal-guardrails"), "the key is the slug of the name")
		Expect(created.ID).To(Equal(EncodeID(KindPreset, hashDir(dir), "personal-guardrails")))
		Expect(created.Name).To(Equal("Personal Guardrails"))
		Expect(created.Description).To(Equal("mine"))
		Expect(created.Scope).To(Equal(api.SpecLayerUser))
		Expect(created.Spec).To(Equal(presetSpecFixture()), "tools and mcp survive the YAML round trip")
		Expect(created.Source.Root).To(Equal(dir))
		Expect(created.UpdatedAt).NotTo(BeZero())

		content, err := os.ReadFile(filepath.Join(dir, "personal-guardrails.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(HavePrefix("name: Personal Guardrails\n"))
		Expect(string(content)).To(ContainSubstring("scope: user\n"))
		Expect(string(content)).To(ContainSubstring("Bash: allow"))
		Expect(string(content)).NotTo(ContainSubstring("id:"), "the id is derived from the file name")
		Expect(string(content)).NotTo(ContainSubstring("updatedAt"))

		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1), "no temp file is left behind")

		_, err = store.Create(ctx, PresetInput{Name: "Zebra", Scope: api.SpecLayerGlobal})
		Expect(err).NotTo(HaveOccurred())
		listed, err := store.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(2))
		Expect(listed[0].Name).To(Equal("Personal Guardrails"))
		Expect(listed[1].Name).To(Equal("Zebra"))

		fetched, err := store.Get(ctx, "personal-guardrails")
		Expect(err).NotTo(HaveOccurred())
		Expect(fetched).To(Equal(created))

		updated, err := store.Update(ctx, "personal-guardrails", PresetInput{Name: "Renamed", Scope: api.SpecLayerContext})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Key).To(Equal("personal-guardrails"), "a rename keeps the key and id")
		Expect(updated.ID).To(Equal(created.ID))
		Expect(updated.Name).To(Equal("Renamed"))
		Expect(updated.Scope).To(Equal(api.SpecLayerContext))
		Expect(updated.Spec).To(Equal(api.RuntimePresetSpec{}), "an update replaces the whole record")

		Expect(store.Delete(ctx, "personal-guardrails")).To(Succeed())
		_, err = store.Get(ctx, "personal-guardrails")
		Expect(err).To(MatchError(ErrNotFound))
		Expect(store.Delete(ctx, "personal-guardrails")).To(MatchError(ErrNotFound))
		_, err = store.Update(ctx, "personal-guardrails", PresetInput{Name: "Ghost", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrNotFound))
	})

	It("refuses a create whose slug is already a file and a name with no slug", func(ctx SpecContext) {
		store := presetDir(GinkgoT().TempDir(), false).Presets()
		_, err := store.Create(ctx, PresetInput{Name: "Team Review", Scope: api.SpecLayerGlobal})
		Expect(err).NotTo(HaveOccurred())
		_, err = store.Create(ctx, PresetInput{Name: "team-review", Scope: api.SpecLayerGlobal})
		Expect(err).To(MatchError(ErrNameTaken))
		_, err = store.Create(ctx, PresetInput{Name: "***", Scope: api.SpecLayerGlobal})
		Expect(err).To(MatchError(ErrInvalid))
	})

	It("rejects invalid input before touching the disk", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		store := presetDir(dir, false).Presets()
		_, err := store.Create(ctx, PresetInput{Name: "", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrInvalid))
		_, err = store.Create(ctx, PresetInput{Name: "Bad scope", Scope: "team"})
		Expect(err).To(MatchError(ErrInvalid))
		Expect(err).To(MatchError(ContainSubstring(`scope "team"`)))
		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})

	It("reports hand-written files that are not valid records", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		store := presetDir(dir, false).Presets()

		writeRecordFile(dir, "with-id.yaml", "id: abc\nname: With id\nscope: user\n")
		_, err := store.Get(ctx, "with-id")
		Expect(err).To(MatchError(ErrInvalid))
		Expect(err).To(MatchError(ContainSubstring("declares an id")))

		writeRecordFile(dir, "unknown-key.yaml", "name: Unknown\nscope: user\nsource: db\n")
		_, err = store.Get(ctx, "unknown-key")
		Expect(err).To(MatchError(ErrInvalid))
		Expect(err).To(MatchError(ContainSubstring("source")))

		writeRecordFile(dir, "bad-scope.yaml", "name: Bad\nscope: team\n")
		_, err = store.Get(ctx, "bad-scope")
		Expect(err).To(MatchError(ErrInvalid))
		Expect(err).To(MatchError(ContainSubstring("bad-scope.yaml")))

		writeRecordFile(dir, "empty.yaml", "")
		_, err = store.Get(ctx, "empty")
		Expect(err).To(MatchError(ContainSubstring("is empty")))

		_, err = store.List(ctx)
		Expect(err).To(MatchError(ErrInvalid), "listing fails loudly rather than skipping a broken file")
	})

	It("refuses a file whose name is not a key and ignores non-record entries", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		store := presetDir(dir, false).Presets()
		writeRecordFile(dir, "ok.yaml", "name: OK\nscope: user\n")
		writeRecordFile(dir, "notes.md", "not a record")
		writeRecordFile(dir, ".ok.yaml.abc.tmp", "name: leftover\nscope: user\n")
		Expect(os.MkdirAll(filepath.Join(dir, "nested.yaml"), 0o755)).To(Succeed())

		listed, err := store.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].Key).To(Equal("ok"))

		_, err = store.Get(ctx, "../ok")
		Expect(err).To(MatchError(ErrNotFound), "a key that is not a key cannot name a record")

		writeRecordFile(dir, "My Preset.yaml", "name: Mine\nscope: user\n")
		_, err = store.List(ctx)
		Expect(err).To(MatchError(ErrInvalid))
		Expect(err).To(MatchError(ContainSubstring(`"My Preset.yaml"`)))
	})

	It("stores profiles with their spec and ordered preset references", func(ctx SpecContext) {
		dir := filepath.Join(GinkgoT().TempDir(), "profiles")
		source, err := NewFileSource(FileSourceOptions{Kind: KindProfile, Dir: dir, Label: "repo", Implicit: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(source.Presets()).To(BeNil())
		Expect(source.Info().Label).To(Equal("repo"))

		spec := api.Spec{
			Model:       api.Model{Name: "gpt-5", Mode: api.ModeAgent},
			Permissions: api.Permissions{Tools: api.Tools{"Bash": api.ToolPolicyAsk}},
			Budget:      api.Budget{MaxTurns: 3},
		}
		created, err := source.Profiles().Create(ctx, ProfileInput{
			Name: "Review", Spec: spec, Presets: []string{" Organization ", EncodeID(KindPreset, "db", "abc")},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created.ID).To(Equal(EncodeID(KindProfile, hashDir(dir), "review")))
		Expect(created.Spec).To(Equal(spec))
		Expect(created.Presets).To(Equal([]string{"Organization", EncodeID(KindPreset, "db", "abc")}))

		content, err := os.ReadFile(filepath.Join(dir, "review.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(content), "presets:")).To(Equal(1))
		Expect(string(content)).To(ContainSubstring("- Organization\n"))

		_, err = source.Profiles().Create(ctx, ProfileInput{Name: "Blank ref", Presets: []string{"a", " "}})
		Expect(err).To(MatchError(ErrInvalid))

		none, err := source.Profiles().Create(ctx, ProfileInput{Name: "No presets"})
		Expect(err).NotTo(HaveOccurred())
		Expect(none.Presets).To(BeEmpty())
		Expect(none.Presets).NotTo(BeNil(), "presets marshal as [] rather than null")
	})
})
