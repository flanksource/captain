package cli

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// seedCaptainConfig points ~/.captain.yaml at a temp file with the given body,
// or at a path that does not exist when body is empty.
func seedCaptainConfig(body string) {
	GinkgoHelper()
	path := filepath.Join(GinkgoT().TempDir(), ".captain.yaml")
	if body != "" {
		Expect(os.WriteFile(path, []byte(body), 0o644)).To(Succeed())
	}
	captainconfig.SetPathForTesting(path)
}

// fixtureHook builds the one hook a declared fixture contributes, through the
// same registry dispatch a run uses.
func fixtureHook(opts verify.Options) *verify.Plugin {
	GinkgoHelper()
	hooks, err := verify.HooksFor(context.Background(),
		&api.Workflow{Verify: &api.Verify{Fixture: "# acceptance\n"}}, opts)
	Expect(err).NotTo(HaveOccurred())
	Expect(hooks).To(HaveLen(1))
	plugin, ok := hooks[0].(*verify.Plugin)
	Expect(ok).To(BeTrue())
	return plugin
}

// The verifier registry is process-global, so every spec here takes its
// registration back out: a leaked `fixture` factory would make the sibling spec
// asserting "no fixture verifier is registered" pass or fail on ordering.
var _ = Describe("installing the configured fixture runner", func() {
	AfterEach(func() {
		verify.Unregister(verify.KindFixture)
		captainconfig.SetPathForTesting("")
		ResetCaptainConfigCache()
	})

	It("claims the fixture kind with the configured runner's argv", func() {
		seedCaptainConfig("verify:\n  fixtureRunner: [gavel, fixtures, --stdin]\n")

		Expect(InstallFixtureVerifier()).To(Succeed())
		Expect(verify.Registered(verify.KindFixture)).To(BeTrue())

		external, ok := fixtureHook(verify.Options{}).Verifier().(*verify.ExternalVerifier)
		Expect(ok).To(BeTrue(), "a configured runner is dispatched as an external process")
		Expect(external.Command).To(Equal([]string{"gavel", "fixtures", "--stdin"}))
		Expect(external.Fixture).To(Equal("# acceptance\n"))
	})

	It("forwards the run's environment, confinement wrapper and timeout to the runner", func() {
		seedCaptainConfig("verify:\n  fixtureRunner: [gavel]\n")
		Expect(InstallFixtureVerifier()).To(Succeed())

		wrap := func(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
			return cmd, args, env, nil
		}
		external, ok := fixtureHook(verify.Options{
			Env: []string{"PATH=/usr/bin"}, Wrap: wrap, Timeout: 42 * time.Second,
		}).Verifier().(*verify.ExternalVerifier)
		Expect(ok).To(BeTrue())
		Expect(external.Env).To(Equal([]string{"PATH=/usr/bin"}))
		Expect(external.Timeout).To(Equal(42 * time.Second))
		Expect(external.Wrap).NotTo(BeNil(),
			"an external runner is agent-adjacent input; it must never escape the run's confinement")
	})

	It("contributes no hook when the workflow declares no fixture", func() {
		seedCaptainConfig("verify:\n  fixtureRunner: [gavel]\n")
		Expect(InstallFixtureVerifier()).To(Succeed())

		hooks, err := verify.HooksFor(context.Background(),
			&api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}, verify.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(hooks).To(HaveLen(1))
		Expect(hooks[0].(*verify.Plugin).Name()).To(Equal("verify:true"))
	})

	It("is a no-op on a host with no config file at all", func() {
		seedCaptainConfig("")

		Expect(InstallFixtureVerifier()).To(Succeed())
		Expect(verify.Registered(verify.KindFixture)).To(BeFalse(),
			"no configured runner means the host runs no fixtures, which HooksFor reports as an error")
	})

	It("fails loudly on a malformed config rather than running no fixtures", func() {
		seedCaptainConfig("verify:\n  fixtureRunner: 42\n")

		Expect(InstallFixtureVerifier()).To(MatchError(ContainSubstring(".captain.yaml")))
		Expect(verify.Registered(verify.KindFixture)).To(BeFalse())
	})

	It("leaves a fixture verifier the host already linked in place", func() {
		seedCaptainConfig("verify:\n  fixtureRunner: [gavel]\n")
		verify.Register(verify.KindFixture, func(
			_ context.Context, _ api.Verify, _ verify.Options,
		) ([]*verify.Plugin, error) {
			return []*verify.Plugin{verify.New("linked-fixture", verify.FuncVerifier(
				func(context.Context, string, []string) (verify.Verdict, error) {
					return verify.Verdict{OK: true}, nil
				}))}, nil
		})

		Expect(InstallFixtureVerifier()).To(Succeed())
		Expect(fixtureHook(verify.Options{}).Name()).To(Equal("linked-fixture"),
			"an in-process runner outranks the configured external one")
	})
})

var _ = Describe("loading ~/.captain.yaml once", func() {
	AfterEach(func() {
		captainconfig.SetPathForTesting("")
		ResetCaptainConfigCache()
	})

	It("parses the file once and serves every later caller from the cache", func() {
		path := filepath.Join(GinkgoT().TempDir(), ".captain.yaml")
		Expect(os.WriteFile(path, []byte("verify:\n  fixtureRunner: [gavel]\n"), 0o644)).To(Succeed())
		captainconfig.SetPathForTesting(path)

		first, exists, err := LoadCaptainConfigOnce()
		Expect(err).NotTo(HaveOccurred())
		Expect(exists).To(BeTrue())
		Expect(first.Verify.FixtureRunner).To(Equal([]string{"gavel"}))

		// Rewriting the file behind the cache is how the second call proves it
		// never re-read: the root command installs several things out of one
		// parse, and they must all see the same file.
		Expect(os.WriteFile(path, []byte("verify:\n  fixtureRunner: [other]\n"), 0o644)).To(Succeed())
		second, _, err := LoadCaptainConfigOnce()
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Verify.FixtureRunner).To(Equal([]string{"gavel"}))

		ResetCaptainConfigCache()
		reloaded, _, err := LoadCaptainConfigOnce()
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Verify.FixtureRunner).To(Equal([]string{"other"}))
	})

	It("re-reads when the config path itself changes", func() {
		seedCaptainConfig("verify:\n  fixtureRunner: [first]\n")
		first, _, err := LoadCaptainConfigOnce()
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Verify.FixtureRunner).To(Equal([]string{"first"}))

		seedCaptainConfig("verify:\n  fixtureRunner: [second]\n")
		second, _, err := LoadCaptainConfigOnce()
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Verify.FixtureRunner).To(Equal([]string{"second"}),
			"the cache is keyed on the path, so a redirected config is never stale")
	})

	It("replays a malformed file's error to every caller", func() {
		seedCaptainConfig("verify:\n  fixtureRunner: 42\n")

		_, _, first := LoadCaptainConfigOnce()
		_, _, second := LoadCaptainConfigOnce()
		Expect(first).To(HaveOccurred())
		Expect(second).To(MatchError(first.Error()),
			"a broken config is a hard stop for the second installer too, not a silent zero value")
	})
})
