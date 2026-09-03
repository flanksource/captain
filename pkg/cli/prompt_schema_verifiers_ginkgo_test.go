package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// hooksForUnregisteredFixtureText is HooksFor's exact refusal, copied rather
// than derived: the webapp renders this sentence, so the document must keep
// saying it even if the availability probe stops asking HooksFor.
const hooksForUnregisteredFixtureText = "workflow.verify.fixture declared but no fixture verifier is registered " +
	"(link a fixture runner in-process or set verify.fixtureRunner in ~/.captain.yaml)"

// fakeFixtureRunner writes an executable that appends one byte to a counter file
// and then prints stdout/stderr and exits with code — enough to prove how many
// times the schema probe actually spawned it.
type fakeFixtureRunner struct {
	path    string
	counter string
}

func newFakeFixtureRunner(stdout, stderr string, code int) fakeFixtureRunner {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	runner := fakeFixtureRunner{
		path:    filepath.Join(dir, "fake-gavel"),
		counter: filepath.Join(dir, "runs"),
	}
	script := "#!/bin/sh\n" +
		"printf x >> " + shellQuote(runner.counter) + "\n" +
		"printf %s " + shellQuote(stdout) + "\n" +
		"printf %s " + shellQuote(stderr) + " 1>&2\n" +
		"exit " + strconv.Itoa(code) + "\n"
	Expect(os.WriteFile(runner.path, []byte(script), 0o755)).To(Succeed())
	return runner
}

// argv is the configured runner argv: the script plus the sub-command a real
// host configures, so the probe is proven to append --schema after both.
func (r fakeFixtureRunner) argv() []string { return []string{r.path, "fixtures", "--stdin"} }

func (r fakeFixtureRunner) runs() int {
	data, err := os.ReadFile(r.counter)
	if os.IsNotExist(err) {
		return 0
	}
	Expect(err).NotTo(HaveOccurred())
	return len(data)
}

func verifierEntry(entries []VerifierAvailability, kind verify.Kind) VerifierAvailability {
	GinkgoHelper()
	for _, entry := range entries {
		if entry.Kind == string(kind) {
			return entry
		}
	}
	Fail("no verifier entry for kind " + string(kind))
	return VerifierAvailability{}
}

// readyAdapter is a probed runtime that can execute a judge prompt.
func readyAdapter() []AdapterStatus {
	return []AdapterStatus{{
		Type: "api", Provider: api.Anthropic.Name, Mode: string(api.ModeAPI), Authenticated: true,
	}}
}

func unauthenticatedAdapter() []AdapterStatus {
	return []AdapterStatus{{Type: "api", Provider: api.Anthropic.Name, Mode: string(api.ModeAPI)}}
}

var _ = Describe("prompt schema verifier availability", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
		ResetFixtureSchemaCache()
	})

	AfterEach(func() {
		verify.Unregister(verify.KindFixture)
		ResetFixtureSchemaCache()
	})

	It("reports cmd available and prompt available when a probed runtime is ready", func() {
		entries, schemas := verifierCatalog(ctx, readyAdapter(), nil)

		Expect(schemas).To(BeNil())
		Expect(verifierEntry(entries, verify.KindCmd)).To(Equal(
			VerifierAvailability{Kind: "cmd", Available: true}))
		Expect(verifierEntry(entries, verify.KindPrompt).Available).To(BeTrue())
		Expect(verifierEntry(entries, verify.KindPrompt).Reason).To(BeEmpty())
	})

	It("reports prompt unavailable when no probed runtime can judge", func() {
		entries, _ := verifierCatalog(ctx, unauthenticatedAdapter(), nil)

		prompt := verifierEntry(entries, verify.KindPrompt)
		Expect(prompt.Available).To(BeFalse())
		Expect(prompt.Reason).To(ContainSubstring("no provider available to judge them"))
	})

	It("reports the fixture kind unavailable with the HooksFor text when nothing is registered", func() {
		entries, schemas := verifierCatalog(ctx, readyAdapter(), nil)

		Expect(schemas).To(BeNil())
		Expect(verifierEntry(entries, verify.KindFixture)).To(Equal(VerifierAvailability{
			Kind: "fixture", Available: false, Reason: hooksForUnregisteredFixtureText,
		}))
	})

	It("reports an in-process registration available without spawning anything", func() {
		runner := newFakeFixtureRunner(`{"schemaVersion":1}`, "", 0)
		verify.Register(verify.KindFixture, func(
			context.Context, api.Verify, verify.Options,
		) ([]*verify.Plugin, error) {
			return nil, nil
		})

		entries, schemas := verifierCatalog(ctx, readyAdapter(), nil)

		Expect(verifierEntry(entries, verify.KindFixture)).To(Equal(
			VerifierAvailability{Kind: "fixture", Available: true}))
		Expect(schemas).To(BeNil(), "there is no runner to ask for fence schemas")
		Expect(runner.runs()).To(Equal(0))
	})

	It("embeds the configured runner's JSON schema document verbatim", func() {
		document := `{"schemaVersion":1,"source":"gavel fixtures --schema","fences":{"test":{"aliases":["yaml test"]}}}`
		runner := newFakeFixtureRunner(document+"\n", "", 0)

		entries, schemas := verifierCatalog(ctx, readyAdapter(), runner.argv())

		Expect(verifierEntry(entries, verify.KindFixture)).To(Equal(
			VerifierAvailability{Kind: "fixture", Available: true}))
		Expect(string(schemas)).To(Equal(document))
		Expect(runner.runs()).To(Equal(1))
	})

	It("names the runner and the parse error when the runner prints garbage", func() {
		runner := newFakeFixtureRunner("not json at all", "", 0)

		entries, schemas := verifierCatalog(ctx, readyAdapter(), runner.argv())

		Expect(schemas).To(BeNil())
		fixture := verifierEntry(entries, verify.KindFixture)
		Expect(fixture.Available).To(BeTrue(),
			"the schema is advisory; a configured runner still claims the kind")
		Expect(fixture.Reason).To(ContainSubstring(runner.path))
		Expect(fixture.Reason).To(ContainSubstring("--schema"))
		Expect(fixture.Reason).To(ContainSubstring("invalid character"))
	})

	It("names the runner and its stderr tail when the runner fails", func() {
		runner := newFakeFixtureRunner("", "unknown flag: --schema", 2)

		entries, schemas := verifierCatalog(ctx, readyAdapter(), runner.argv())

		Expect(schemas).To(BeNil())
		fixture := verifierEntry(entries, verify.KindFixture)
		Expect(fixture.Available).To(BeTrue())
		Expect(fixture.Reason).To(ContainSubstring("exit status 2"))
		Expect(fixture.Reason).To(ContainSubstring("unknown flag: --schema"))
	})

	It("spawns the runner once per argv and serves later documents from the cache", func() {
		runner := newFakeFixtureRunner(`{"schemaVersion":1}`, "", 0)

		_, first := verifierCatalog(ctx, readyAdapter(), runner.argv())
		_, second := verifierCatalog(ctx, readyAdapter(), runner.argv())

		Expect(string(second)).To(Equal(string(first)))
		Expect(runner.runs()).To(Equal(1))

		ResetFixtureSchemaCache()
		_, third := verifierCatalog(ctx, readyAdapter(), runner.argv())
		Expect(string(third)).To(Equal(string(first)))
		Expect(runner.runs()).To(Equal(2), "the reset seam re-runs the probe for the next spec")
	})
})

var _ = Describe("prompt schema document verifier fields", func() {
	var previousAdapters func() ([]AdapterStatus, error)

	BeforeEach(func() {
		ResetFixtureSchemaCache()
		previousAdapters = schemaAdapters
		schemaAdapters = func() ([]AdapterStatus, error) {
			return ai.ProbeAdapters(ai.WhoamiOptions{}, ai.AuthProbe{
				Getenv:     func(string) string { return "" },
				LookPath:   func(bin string) (string, error) { return "/usr/local/bin/" + bin, nil },
				FileExists: func(string) bool { return false },
				Home:       "/home/test",
			})
		}
	})

	AfterEach(func() {
		schemaAdapters = previousAdapters
		verify.Unregister(verify.KindFixture)
		captainconfig.SetPathForTesting("")
		ResetCaptainConfigCache()
		ResetFixtureSchemaCache()
	})

	It("serves verifiers[] and the configured runner's fixtureSchemas", func() {
		runner := newFakeFixtureRunner(`{"schemaVersion":1,"fences":{}}`, "", 0)
		seedCaptainConfig("verify:\n  fixtureRunner: [" + runner.path + ", fixtures]\n")

		doc, err := PromptSchemaDocument(context.Background())
		Expect(err).NotTo(HaveOccurred())

		entries, ok := doc["verifiers"].([]VerifierAvailability)
		Expect(ok).To(BeTrue(), "document carries typed verifier availability")
		Expect(entries).To(HaveLen(3))
		Expect(verifierEntry(entries, verify.KindFixture).Available).To(BeTrue())

		raw, ok := doc["fixtureSchemas"].(json.RawMessage)
		Expect(ok).To(BeTrue())
		Expect(string(raw)).To(Equal(`{"schemaVersion":1,"fences":{}}`))

		encoded, err := json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		var decoded map[string]any
		Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
		Expect(decoded["fixtureSchemas"]).To(HaveKeyWithValue("schemaVersion", float64(1)))
	})

	It("omits fixtureSchemas when no runner is configured", func() {
		seedCaptainConfig("")

		doc, err := PromptSchemaDocument(context.Background())
		Expect(err).NotTo(HaveOccurred())

		Expect(doc).NotTo(HaveKey("fixtureSchemas"))
		entries := doc["verifiers"].([]VerifierAvailability)
		Expect(verifierEntry(entries, verify.KindFixture).Reason).To(Equal(hooksForUnregisteredFixtureText))
	})
})
