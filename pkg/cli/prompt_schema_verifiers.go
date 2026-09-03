package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
)

// VerifierAvailability is one verify kind and whether this host can actually run
// it. The webapp authors `workflow.verify.*` against it: a kind that cannot run
// here must be shown as such before a run declares it, because HooksFor refuses
// a declared check with no factory rather than passing vacuously.
type VerifierAvailability struct {
	Kind      string `json:"kind"`
	Available bool   `json:"available"`
	// Reason is empty when the kind is available and nothing went wrong. On an
	// unavailable kind it is the runtime's own refusal text; on an available
	// fixture kind it can still carry a schema-probe failure, which is advisory.
	Reason string `json:"reason"`
}

// fixtureSchemaProbeTimeout bounds one `<runner> --schema` call. The schema
// document is served per request, so a runner that hangs must not hang the
// editor; ten seconds is far more than printing a reflected schema takes.
const fixtureSchemaProbeTimeout = 10 * time.Second

// verifierCatalog answers, for every kind in the verify registry, whether this
// host can run it, and — when a fixture runner is configured — that runner's own
// fence schemas so the editor can complete a fixture document.
//
// It never fails: a broken fixture runner is reported on the fixture entry, not
// raised, because the rest of the schema document is still correct and the
// editor still needs it.
func verifierCatalog(ctx context.Context, adapters []AdapterStatus, runner []string) ([]VerifierAvailability, json.RawMessage) {
	fixture, schemas := fixtureVerifierAvailability(ctx, runner)
	return []VerifierAvailability{
		// cmd is captain's own factory, registered in the verify package's init:
		// there is no host state that can take it away.
		{Kind: string(verify.KindCmd), Available: true},
		promptVerifierAvailability(adapters),
		fixture,
	}, schemas
}

// promptVerifierAvailability reads the judge's precondition off the adapters the
// document already probed: promptFactory needs a provider, and a provider needs
// a runtime that is authenticated and installed. With nothing probed there is no
// evidence against it, so the kind is offered.
func promptVerifierAvailability(adapters []AdapterStatus) VerifierAvailability {
	entry := VerifierAvailability{Kind: string(verify.KindPrompt), Available: true}
	if len(adapters) == 0 {
		return entry
	}
	for _, adapter := range adapters {
		if adapter.Ready() {
			return entry
		}
	}
	entry.Available = false
	entry.Reason = "verify prompts declared but no provider available to judge them: " +
		"no probed runtime is authenticated (run 'captain whoami')"
	return entry
}

// fixtureVerifierAvailability reports whether a declared fixture would dispatch,
// and fetches the configured runner's schemas when there is one to ask.
//
// The two are independent: the kind is claimed by an in-process registration or
// by the configured argv, while the schemas are advisory editor metadata. A
// runner that cannot print its schemas still runs fixtures.
func fixtureVerifierAvailability(ctx context.Context, runner []string) (VerifierAvailability, json.RawMessage) {
	entry := VerifierAvailability{Kind: string(verify.KindFixture)}
	entry.Available = verify.Registered(verify.KindFixture) || len(runner) > 0
	if !entry.Available {
		entry.Reason = fixtureUnavailableReason(ctx)
		return entry, nil
	}
	if len(runner) == 0 {
		return entry, nil
	}
	schemas, err := fixtureSchemas(runner)
	if err != nil {
		entry.Reason = err.Error()
		return entry, nil
	}
	return entry, schemas
}

// fixtureUnavailableReason is HooksFor's own refusal, asked rather than
// re-worded: the editor shows the exact sentence a run would fail with. The
// probe workflow declares only a fixture, so dispatch stops at the registry
// check and nothing is executed.
func fixtureUnavailableReason(ctx context.Context) string {
	_, err := verify.HooksFor(ctx,
		&api.Workflow{Verify: &api.Verify{Fixture: "# availability probe\n"}}, verify.Options{})
	if err != nil {
		return err.Error()
	}
	return ""
}

// fixtureSchemaEntry memoizes one runner argv's schema probe — result or
// failure — so a serve process answers the document endpoint without spawning a
// process per request.
type fixtureSchemaEntry struct {
	once    sync.Once
	schemas json.RawMessage
	err     error
}

var fixtureSchemaCache sync.Map // argv key -> *fixtureSchemaEntry

func fixtureSchemas(runner []string) (json.RawMessage, error) {
	value, _ := fixtureSchemaCache.LoadOrStore(strings.Join(runner, "\x00"), &fixtureSchemaEntry{})
	entry := value.(*fixtureSchemaEntry)
	entry.once.Do(func() { entry.schemas, entry.err = probeFixtureSchemas(runner) })
	return entry.schemas, entry.err
}

// ResetFixtureSchemaCache drops every memoized schema probe. It exists for specs
// that swap the configured runner within one process; a serve process keeps the
// cache for its lifetime, since the runner argv comes from a config file read at
// startup.
func ResetFixtureSchemaCache() {
	fixtureSchemaCache.Range(func(key, _ any) bool {
		fixtureSchemaCache.Delete(key)
		return true
	})
}

// probeFixtureSchemas runs `<runner argv> --schema` and returns its stdout
// verbatim once it is confirmed to be JSON. The context is this call's own, not
// the request's: the result is cached process-wide, so one cancelled HTTP
// request must not poison every later document with a context error.
func probeFixtureSchemas(runner []string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fixtureSchemaProbeTimeout)
	defer cancel()

	args := append(append([]string{}, runner[1:]...), "--schema")
	cmd := exec.CommandContext(ctx, runner[0], args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	label := strings.Join(append(append([]string{}, runner...), "--schema"), " ")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w%s", label, err, stderrTail(stderr.String()))
	}
	schemas := bytes.TrimSpace(stdout.Bytes())
	var probe any
	if err := json.Unmarshal(schemas, &probe); err != nil {
		return nil, fmt.Errorf("%s: printed no JSON schema document: %w", label, err)
	}
	return json.RawMessage(schemas), nil
}

// stderrTail is the last few lines of a failed runner's stderr — enough to name
// the failure without pasting a whole run into the schema document.
func stderrTail(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	tail := strings.TrimSpace(strings.Join(lines, "\n"))
	if tail == "" {
		return ""
	}
	return ": " + tail
}
