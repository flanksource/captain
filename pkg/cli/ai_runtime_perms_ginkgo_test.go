package cli

import (
	"context"
	"testing/fstest"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/clicky"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

const hostPermName = "Acme debug"

// permsFixture pins a catalog holding a host's embedded preset beside
// captain's built-ins, so Resolve never opens the CLI database.
type permsFixture struct {
	ctx    context.Context
	layers []api.SpecLayer
}

func newPermsFixture() permsFixture {
	GinkgoHelper()
	host, err := runtimeprofiles.NewEmbeddedSource(fstest.MapFS{
		"presets/acme-debug.yaml": {Data: []byte("name: " + hostPermName + "\nscope: context\nspec:\n  permissions:\n    mode: acceptEdits\n    tools:\n      web_fetch: deny\n")},
	}, "presets", runtimeprofiles.SourceInfo{ID: "acme", Label: "Acme"})
	Expect(err).NotTo(HaveOccurred())
	catalog, err := runtimeprofiles.NewCatalog(host, runtimeprofiles.NewBuiltinSource())
	Expect(err).NotTo(HaveOccurred())
	return permsFixture{
		ctx: ContextWithRuntimeCatalog(context.Background(), catalog),
		layers: []api.SpecLayer{{Name: "host prompt", Scope: api.SpecLayerGlobal, Spec: api.Spec{
			Prompt: api.Prompt{User: "Fix the fixture"}, Permissions: api.Permissions{Mode: api.PermissionDefault},
		}}},
	}
}

func (f permsFixture) resolve(options AIRuntimeOptions, defaults []string) (AIRuntimeResolved, error) {
	return options.Resolve(AIRuntimeResolveOptions{
		Context: f.ctx, Layers: f.layers, Saved: captainconfig.Config{}, Cwd: GinkgoT().TempDir(), DefaultPerms: defaults,
	})
}

func selectedPermNames(resolved AIRuntimeResolved) []string {
	if resolved.Presets == nil {
		return nil
	}
	names := []string{}
	for _, preset := range resolved.Presets.Presets {
		names = append(names, preset.Name)
	}
	return names
}

var _ = Describe("AIRuntimeOptions --perms", func() {
	It("layers the host's default perms over the host layers and records the selection", func() {
		f := newPermsFixture()
		resolved, err := f.resolve(AIRuntimeOptions{}, []string{hostPermName})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Request.Permissions.Mode).To(Equal(api.PermissionAcceptEdits))
		Expect(resolved.Request.Permissions.Tools).To(HaveKeyWithValue("web_fetch", api.ToolPolicyDeny))
		Expect(selectedPermNames(resolved)).To(Equal([]string{hostPermName}))
		Expect(traceNames(resolved.Resolution)).To(Equal([]string{"host prompt", hostPermName}))
	})

	It("replaces the host default with an explicit --perms selection", func() {
		f := newPermsFixture()
		resolved, err := f.resolve(AIRuntimeOptions{Perms: []string{"plan"}}, []string{hostPermName})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Request.Permissions.Mode).To(Equal(api.PermissionPlan))
		Expect(resolved.Request.Permissions.Tools).NotTo(HaveKey("web_fetch"), "the default's tools are not layered")
		Expect(selectedPermNames(resolved)).To(Equal([]string{"Plan"}))
	})

	DescribeTable("selects no perms when the selection is explicitly empty",
		func(perms []string) {
			f := newPermsFixture()
			resolved, err := (AIRuntimeOptions{Perms: perms}).Resolve(AIRuntimeResolveOptions{
				Layers: f.layers, Cwd: GinkgoT().TempDir(), DefaultPerms: []string{hostPermName},
			})
			Expect(err).NotTo(HaveOccurred(), "an empty selection needs no catalog and so no context")
			Expect(resolved.Request.Permissions.Mode).To(Equal(api.PermissionDefault))
			Expect(resolved.Presets).To(BeNil())
			Expect(traceNames(resolved.Resolution)).To(Equal([]string{"host prompt"}))
		},
		Entry("no entries", []string{}),
		Entry("one empty string", []string{""}),
		Entry("blank entries", []string{" ", ""}),
	)

	It("lets an explicit --permission-mode beat a perm's mode while the perm keeps its tools", func() {
		f := newPermsFixture()
		resolved, err := f.resolve(AIRuntimeOptions{PermissionMode: string(api.PermissionPlan)}, []string{hostPermName})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Request.Permissions.Mode).To(Equal(api.PermissionPlan))
		Expect(resolved.Request.Permissions.Tools).To(HaveKeyWithValue("web_fetch", api.ToolPolicyDeny))
		Expect(traceNames(resolved.Resolution)).To(Equal([]string{"host prompt", hostPermName, "CLI flags"}))
	})

	It("fails on an unknown perm and lists the available names", func() {
		f := newPermsFixture()
		_, err := f.resolve(AIRuntimeOptions{Perms: []string{"Read-only", "nope"}}, nil)
		Expect(err).To(MatchError(ContainSubstring(`"nope"`)))
		Expect(err).To(MatchError(ContainSubstring("available perms: " + hostPermName + ", Edit, Plan, Read-only")))
		Expect(err).To(MatchError(runtimeprofiles.ErrNotFound))
	})

	It("fails on an unknown default perm, naming it as the host default", func() {
		f := newPermsFixture()
		_, err := f.resolve(AIRuntimeOptions{}, []string{"missing"})
		Expect(err).To(MatchError(ContainSubstring("selected by default")))
		Expect(err).To(MatchError(ContainSubstring("available perms:")))
	})

	It("requires a context once perms are selected", func() {
		f := newPermsFixture()
		_, err := (AIRuntimeOptions{Perms: []string{"plan"}}).Resolve(AIRuntimeResolveOptions{Layers: f.layers, Cwd: GinkgoT().TempDir()})
		Expect(err).To(MatchError(ContainSubstring("Context")))
	})

	DescribeTable("binds --perms through clicky so unset and explicitly empty differ",
		func(args []string, expected []string) {
			var bound AIRuntimeOptions
			root := &cobra.Command{Use: "host", SilenceUsage: true, SilenceErrors: true}
			clicky.AddNamedCommand("probe", root, AIRuntimeOptions{}, func(options AIRuntimeOptions) (any, error) {
				bound = options
				return nil, nil
			})
			root.SetArgs(append([]string{"probe"}, args...))
			Expect(root.Execute()).To(Succeed())
			if expected == nil {
				Expect(bound.Perms).To(BeNil())
				return
			}
			Expect(bound.Perms).To(Equal(expected))
		},
		Entry("unset uses the host default", []string{}, nil),
		Entry("an empty value selects none", []string{"--perms", ""}, []string{}),
		Entry("repeatable", []string{"--perms", "plan", "--perms", hostPermName}, []string{"plan", hostPermName}),
	)
})
