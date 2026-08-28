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

	off := catalogKind(t, catalog, "off")
	if len(off.Capabilities) != 0 {
		t.Errorf("off capabilities = %v, want empty", off.Capabilities)
	}
	if len(off.Modes) != len(api.AllRuntimeModes()) {
		t.Errorf("off modes = %v, want every runtime mode", off.Modes)
	}
}

func TestBuildSandboxCatalogNestsConfiguredBackendsUnderTheirKind(t *testing.T) {
	catalog := buildSandboxCatalog(captainconfig.SandboxDefaults{
		Backends: map[string]captainconfig.SandboxBackend{
			"prod-pool":    gitAgentBackendFixture(),
			"local-docker": {Kind: "docker"},
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

	if docker := catalogKind(t, catalog, "docker"); len(docker.Backends) != 1 {
		t.Errorf("docker backends = %+v, want just local-docker", docker.Backends)
	}
	if native := catalogKind(t, catalog, "native"); len(native.Backends) != 0 {
		t.Errorf("native backends = %+v, want none configured", native.Backends)
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
	bare := buildSandboxCatalog(captainconfig.SandboxDefaults{Default: "native"})
	if !catalogKind(t, bare, "native").Default {
		t.Error("native kind should be flagged as the default")
	}
}

func TestBuildSandboxCatalogReportsBackendsWithAnUnusableKind(t *testing.T) {
	catalog := buildSandboxCatalog(captainconfig.SandboxDefaults{
		Backends: map[string]captainconfig.SandboxBackend{
			"typo":    {Kind: "git-agnet"},
			"kindles": {Kind: "  "},
			"good":    {Kind: "native"},
		},
	})

	if len(catalog.Invalid) != 2 {
		t.Fatalf("catalog.Invalid = %+v, want the two unusable backends", catalog.Invalid)
	}
	byName := map[string]SandboxBackendEntry{}
	for _, entry := range catalog.Invalid {
		byName[entry.Name] = entry
	}
	// An empty kind must NOT resolve to "off": ParseSandboxKind maps "" to off
	// because an absent selector means unconfined, but a backend that declares no
	// kind is a mistake, and silently running unsandboxed is the failure the
	// descriptor table exists to prevent.
	if got := byName["kindles"].Error; got == "" {
		t.Error("a backend with a blank kind must report an error, not resolve to off")
	}
	if got := byName["typo"].Error; got == "" {
		t.Error("a backend with an unknown kind must report an error")
	}
	// Valid backends are unaffected by an invalid sibling.
	if native := catalogKind(t, catalog, "native"); len(native.Backends) != 1 {
		t.Errorf("native backends = %+v, want the valid 'good' backend", native.Backends)
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

	modeBySelector := map[string][]any{}
	for _, raw := range spec["allOf"].([]any) {
		rule := raw.(map[string]any)
		condition := rule["if"].(map[string]any)
		scalar := condition["anyOf"].([]any)[0].(map[string]any)
		selector := scalar["properties"].(map[string]any)["sandbox"].(map[string]any)["const"].(string)
		then := rule["then"].(map[string]any)["properties"].(map[string]any)
		modeBySelector[selector] = then["mode"].(map[string]any)["enum"].([]any)
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
	// Docker wraps only the CLI exec site.
	if want := []any{"", "cli"}; !reflect.DeepEqual(modeBySelector["docker"], want) {
		t.Errorf("docker mode enum = %v, want %v", modeBySelector["docker"], want)
	}
	// Off serves every mode, so it needs no rule at all.
	if enum, ok := modeBySelector["off"]; ok {
		t.Errorf("off should emit no mode constraint, got %v", enum)
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
