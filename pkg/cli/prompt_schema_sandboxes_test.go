package cli

import (
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// gitAgentBackendFixture is a configured git-agent backend carrying one fully
// enrolled agent, one enrolled without a host key (recorded by hand, so not
// dispatchable), and one whose workload captain placed but which has not
// enrolled back yet.
func gitAgentBackendFixture() captainconfig.SandboxBackend {
	return captainconfig.SandboxBackend{
		Kind: "git-agent",
		Options: map[string]any{
			"url": "ssh://supervisor.internal:7422",
			"agents": map[string]any{
				"worker-01": map[string]any{
					"fingerprint":     "SHA256:aaa",
					"url":             "ssh://worker-01:7422",
					"hostFingerprint": "SHA256:bbb",
					"addedAt":         "2026-08-01T00:00:00Z",
				},
				"worker-02": map[string]any{
					"fingerprint": "SHA256:ccc",
					"url":         "ssh://worker-02:7422",
				},
			},
			// A deployment with no matching agent entry is the only "not yet
			// enrolled" state there is: tokens are durable, so the pending-join
			// record they used to need is gone.
			"deployments": map[string]any{
				"worker-03": map[string]any{
					"target":     "docker",
					"workload":   "captain-git-agent-worker-03",
					"image":      "ghcr.io/flanksource/captain:latest",
					"deployedAt": "2026-08-02T00:00:00Z",
				},
			},
		},
	}
}

func catalogKind(t *testing.T, catalog SandboxCatalog, kind string) SandboxCatalogEntry {
	t.Helper()
	for _, entry := range catalog.Kinds {
		if entry.Kind == kind {
			return entry
		}
	}
	t.Fatalf("catalog has no %q entry; got %+v", kind, catalog.Kinds)
	return SandboxCatalogEntry{}
}

func TestBuildSandboxCatalogListsEveryAdapterInCanonicalOrder(t *testing.T) {
	catalog := buildSandboxCatalog(captainconfig.SandboxDefaults{})

	got := make([]string, 0, len(catalog.Kinds))
	for _, entry := range catalog.Kinds {
		got = append(got, entry.Kind)
	}
	want := make([]string, 0, len(api.AllSandboxes()))
	for _, descriptor := range api.AllSandboxes() {
		want = append(want, string(descriptor.Kind))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("catalog kinds = %v, want %v (canonical AllSandboxes order)", got, want)
	}
}

func TestBuildSandboxCatalogProjectsDescriptorCapabilitiesAndModes(t *testing.T) {
	catalog := buildSandboxCatalog(captainconfig.SandboxDefaults{})

	gitAgent := catalogKind(t, catalog, "git-agent")
	wantCapabilities := []string{"remote-exec", "isolate-workspace", "egress-proxy"}
	if !reflect.DeepEqual(gitAgent.Capabilities, wantCapabilities) {
		t.Errorf("git-agent capabilities = %v, want %v", gitAgent.Capabilities, wantCapabilities)
	}
	// git-agent deliberately excludes ModeAPI: its contract is that work returns
	// as commits, and a direct API call produces no working-tree change to push.
	wantModes := []string{"cli", "agent", "cmux"}
	if !reflect.DeepEqual(gitAgent.Modes, wantModes) {
		t.Errorf("git-agent modes = %v, want %v", gitAgent.Modes, wantModes)
	}
	if gitAgent.Description == "" {
		t.Error("git-agent description is empty; the editor renders it as help text")
	}

	none := catalogKind(t, catalog, "none")
	if len(none.Capabilities) != 0 {
		t.Errorf("none capabilities = %v, want empty", none.Capabilities)
	}
	if len(none.Modes) != len(api.AllRuntimeModes()) {
		t.Errorf("none modes = %v, want every runtime mode", none.Modes)
	}
}

func TestBuildSandboxCatalogNestsConfiguredBackendsUnderTheirKind(t *testing.T) {
	catalog := buildSandboxCatalog(captainconfig.SandboxDefaults{
		Backends: map[string]captainconfig.SandboxBackend{
			"prod-pool":    gitAgentBackendFixture(),
			"local-docker": {Kind: "container"},
		},
	})

	gitAgent := catalogKind(t, catalog, "git-agent")
	if len(gitAgent.Backends) != 1 || gitAgent.Backends[0].Name != "prod-pool" {
		t.Fatalf("git-agent backends = %+v, want just prod-pool", gitAgent.Backends)
	}
	pool := gitAgent.Backends[0]
	if pool.URL != "ssh://supervisor.internal:7422" {
		t.Errorf("prod-pool url = %q, want the configured endpoint", pool.URL)
	}
	if pool.Kind != "git-agent" {
		t.Errorf("prod-pool kind = %q, want git-agent", pool.Kind)
	}

	if container := catalogKind(t, catalog, "container"); len(container.Backends) != 1 {
		t.Errorf("container backends = %+v, want just local-docker", container.Backends)
	}
	if srt := catalogKind(t, catalog, "srt"); len(srt.Backends) != 0 {
		t.Errorf("srt backends = %+v, want none configured", srt.Backends)
	}
	if len(catalog.Invalid) != 0 {
		t.Errorf("catalog.Invalid = %+v, want empty", catalog.Invalid)
	}
}

func TestBuildSandboxCatalogSurfacesTheGitAgentRoster(t *testing.T) {
	catalog := buildSandboxCatalog(captainconfig.SandboxDefaults{
		Backends: map[string]captainconfig.SandboxBackend{"prod-pool": gitAgentBackendFixture()},
	})

	agents := catalogKind(t, catalog, "git-agent").Backends[0].Agents
	want := []GitAgentListEntry{
		{
			Name:            "worker-01",
			Fingerprint:     "SHA256:aaa",
			URL:             "ssh://worker-01:7422",
			HostFingerprint: "SHA256:bbb",
			AddedAt:         "2026-08-01T00:00:00Z",
			Status:          "enrolled",
			Dispatchable:    true,
		},
		// Enrolled but missing a host fingerprint: the adapter cannot pin its host
		// key, so dispatch fails. The roster reports it rather than hiding it.
		{
			Name:          "worker-02",
			Fingerprint:   "SHA256:ccc",
			URL:           "ssh://worker-02:7422",
			Status:        "enrolled",
			DispatchIssue: "missing host key",
		},
		// Deployed but not yet joined. Listed so an operator who just deployed
		// sees the workload rather than an empty roster and no way to remove it.
		{
			Name:   "worker-03",
			Status: "deployed — waiting to enroll",
			Deployment: &GitAgentDeployment{
				Target:     "docker",
				Workload:   "captain-git-agent-worker-03",
				Image:      "ghcr.io/flanksource/captain:latest",
				DeployedAt: "2026-08-02T00:00:00Z",
			},
		},
	}
	if !reflect.DeepEqual(agents, want) {
		t.Errorf("roster = %+v, want %+v", agents, want)
	}
}

func TestBuildSandboxCatalogMarksTheConfiguredDefault(t *testing.T) {
	defaults := captainconfig.SandboxDefaults{
		Default:  "prod-pool",
		Backends: map[string]captainconfig.SandboxBackend{"prod-pool": gitAgentBackendFixture()},
	}
	catalog := buildSandboxCatalog(defaults)
	if catalog.Default != "prod-pool" {
		t.Errorf("catalog.Default = %q, want prod-pool", catalog.Default)
	}
	if !catalogKind(t, catalog, "git-agent").Backends[0].Default {
		t.Error("prod-pool backend should be flagged as the default")
	}
	if catalogKind(t, catalog, "git-agent").Default {
		t.Error("the bare git-agent kind is not the default; the backend name is")
	}

	// A bare kind as the default flags the kind, not any backend.
	bare := buildSandboxCatalog(captainconfig.SandboxDefaults{Default: "srt"})
	if !catalogKind(t, bare, "srt").Default {
		t.Error("srt kind should be flagged as the default")
	}
}

func TestBuildSandboxCatalogReportsBackendsWithAnUnusableKind(t *testing.T) {
	catalog := buildSandboxCatalog(captainconfig.SandboxDefaults{
		Backends: map[string]captainconfig.SandboxBackend{
			"typo":    {Kind: "git-agnet"},
			"kindles": {Kind: "  "},
			"good":    {Kind: "srt"},
		},
	})

	if len(catalog.Invalid) != 2 {
		t.Fatalf("catalog.Invalid = %+v, want the two unusable backends", catalog.Invalid)
	}
	byName := map[string]SandboxBackendEntry{}
	for _, entry := range catalog.Invalid {
		byName[entry.Name] = entry
	}
	// An empty kind must NOT resolve to "none": ParseSandboxKind maps "" to none
	// because an absent selector means unconfined, but a backend that declares no
	// kind is a mistake, and silently running unsandboxed is the failure the
	// descriptor table exists to prevent.
	if got := byName["kindles"].Error; got == "" {
		t.Error("a backend with a blank kind must report an error, not resolve to none")
	}
	if got := byName["typo"].Error; got == "" {
		t.Error("a backend with an unknown kind must report an error")
	}
	// Valid backends are unaffected by an invalid sibling.
	if srt := catalogKind(t, catalog, "srt"); len(srt.Backends) != 1 {
		t.Errorf("srt backends = %+v, want the valid 'good' backend", srt.Backends)
	}
	// And an unusable backend is never offered as a selectable choice.
	for _, entry := range catalog.Kinds {
		for _, backend := range entry.Backends {
			if backend.Name == "typo" || backend.Name == "kindles" {
				t.Errorf("unusable backend %q offered under kind %q", backend.Name, entry.Kind)
			}
		}
	}
}

func TestPromptSchemaSandboxModeConditionals(t *testing.T) {
	defaults := captainconfig.SandboxDefaults{Backends: map[string]captainconfig.SandboxBackend{
		"prod-pool": {Kind: "git-agent"},
	}}
	doc, err := buildPromptSchemaDocument(stubbedSchemaAdapters(t), defaults)
	if err != nil {
		t.Fatalf("buildPromptSchemaDocument: %v", err)
	}
	spec := doc["spec"].(map[string]any)

	// Regression against injectSpecConditionals' `specMap["allOf"] = allOf`
	// assignment: appending the sandbox rules must not drop the backend rules.
	backendRules := 0
	modeBySelector := map[string][]any{}
	for _, raw := range spec["allOf"].([]any) {
		rule := raw.(map[string]any)
		condition := rule["if"].(map[string]any)
		if props, ok := condition["properties"].(map[string]any); ok {
			if _, isBackend := props["backend"]; isBackend {
				backendRules++
				continue
			}
		}
		scalar := condition["anyOf"].([]any)[0].(map[string]any)
		selector := scalar["properties"].(map[string]any)["sandbox"].(map[string]any)["const"].(string)
		then := rule["then"].(map[string]any)["properties"].(map[string]any)
		modeBySelector[selector] = then["mode"].(map[string]any)["enum"].([]any)
	}
	if backendRules != len(api.AllBackends()) {
		t.Errorf("backend conditionals = %d, want %d; the sandbox rules overwrote them",
			backendRules, len(api.AllBackends()))
	}

	// git-agent cannot serve ModeAPI, so choosing it constrains mode.
	wantGitAgent := []any{"", "cli", "agent", "cmux"}
	if !reflect.DeepEqual(modeBySelector["git-agent"], wantGitAgent) {
		t.Errorf("git-agent mode enum = %v, want %v", modeBySelector["git-agent"], wantGitAgent)
	}
	// A configured backend gets the same constraint as the kind it resolves to.
	if !reflect.DeepEqual(modeBySelector["prod-pool"], wantGitAgent) {
		t.Errorf("prod-pool mode enum = %v, want %v", modeBySelector["prod-pool"], wantGitAgent)
	}
	// srt and container only hook the CLI exec site.
	if want := []any{"", "cli"}; !reflect.DeepEqual(modeBySelector["srt"], want) {
		t.Errorf("srt mode enum = %v, want %v", modeBySelector["srt"], want)
	}
	// "none" serves every mode, so it needs no rule at all.
	if enum, ok := modeBySelector["none"]; ok {
		t.Errorf("none should emit no mode constraint, got %v", enum)
	}
}

// A scalar `sandbox:` must match only its own rule. Without an explicit
// "type": "object" on the object branch, `required: [backend]` is vacuously
// true for a string, every selector's rule fires at once, and mode collapses to
// the intersection of all adapters.
func TestSandboxModeRuleObjectBranchIsTypePinned(t *testing.T) {
	rule := sandboxModeRule("git-agent", []any{"", "cli"})
	branches := rule["if"].(map[string]any)["anyOf"].([]any)

	scalar := branches[0].(map[string]any)["properties"].(map[string]any)["sandbox"].(map[string]any)
	if scalar["type"] != "string" {
		t.Errorf("scalar branch type = %v, want string", scalar["type"])
	}
	object := branches[1].(map[string]any)["properties"].(map[string]any)["sandbox"].(map[string]any)
	if object["type"] != "object" {
		t.Errorf("object branch type = %v, want object", object["type"])
	}
}

func TestPromptSchemaDocumentServesTheSandboxCatalog(t *testing.T) {
	defaults := captainconfig.SandboxDefaults{
		Default:  "prod-pool",
		Backends: map[string]captainconfig.SandboxBackend{"prod-pool": gitAgentBackendFixture()},
	}
	doc, err := buildPromptSchemaDocument(stubbedSchemaAdapters(t), defaults)
	if err != nil {
		t.Fatalf("buildPromptSchemaDocument: %v", err)
	}
	catalog, ok := doc["sandboxes"].(SandboxCatalog)
	if !ok {
		t.Fatalf("doc[sandboxes] = %T, want SandboxCatalog", doc["sandboxes"])
	}
	if catalog.Default != "prod-pool" {
		t.Errorf("served default = %q, want prod-pool", catalog.Default)
	}
	if len(catalog.Kinds) != len(api.AllSandboxes()) {
		t.Errorf("served kinds = %d, want %d", len(catalog.Kinds), len(api.AllSandboxes()))
	}
}
