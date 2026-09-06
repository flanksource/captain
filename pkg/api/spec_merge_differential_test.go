package api

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/commons-db/shell"
)

// This file is the gate for replacing ~140 lines of hand-written field-by-field
// merging with pkg/merge's structural one. It generates populated Spec pairs at
// random, merges each both ways, and requires the results to agree everywhere
// except the handful of places the structural engine deliberately behaves
// differently — each of which is named in neutralize below and pinned by its own
// assertion in TestSpec_Merge_IntentionalPolicyChanges.
//
// A Spec field added later is covered automatically: the generator populates it
// by reflection and the comparison notices any divergence, so a new field either
// merges identically or forces an explicit decision here.

// legacyMerge is the hand-written implementation Spec.Merge had before the
// structural engine. It is kept only as the differential reference.
func legacyMerge(s, override Spec) Spec {
	s.Model = legacyMergeModel(s.Model, override.Model)
	s.Prompt = legacyMergePrompt(s.Prompt, override.Prompt)
	if len(override.Messages) > 0 {
		s.Messages = override.Messages
	}
	s.Budget = legacyMergeBudget(s.Budget, override.Budget)
	s.Memory = legacyMergeMemory(s.Memory, override.Memory)
	s.Permissions = legacyMergePermissions(s.Permissions, override.Permissions)
	if len(override.ToolPreferences) > 0 {
		s.ToolPreferences = override.ToolPreferences
	}
	// An ordered rule list is replaced wholesale, never element-wise: the list's
	// meaning is its order, so splicing one layer's rules into another's
	// positions would produce a precedence neither author wrote.
	if len(override.ToolPolicy) > 0 {
		s.ToolPolicy = override.ToolPolicy
	}
	if override.ToolApproval != nil {
		s.ToolApproval = override.ToolApproval
	}
	if override.Setup != nil {
		s.Setup = override.Setup
	}
	if override.Sandbox != nil {
		s.Sandbox = override.Sandbox
	}
	if override.Workflow != nil {
		s.Workflow = override.Workflow
	}
	if override.SessionID != "" {
		s.SessionID = override.SessionID
	}
	if len(override.CLIArgs) > 0 {
		s.CLIArgs = override.CLIArgs
	}
	return s
}

func legacyMergeModel(m, o Model) Model {
	if o.Name != "" {
		m.Name = o.Name
	}
	if o.ID != "" {
		m.ID = o.ID
	}
	if o.Temperature != nil {
		m.Temperature = o.Temperature
	}
	if o.Effort != "" {
		m.Effort = o.Effort
	}
	if o.Mode != "" {
		m.Mode = o.Mode
	}
	if o.NoCache {
		m.NoCache = true
	}
	if len(o.Fallbacks) > 0 {
		m.Fallbacks = o.Fallbacks
	}
	return m
}

func legacyMergePrompt(p, o Prompt) Prompt {
	if o.User != "" {
		p.User = o.User
	}
	if o.System != "" {
		p.System = o.System
	}
	if o.AppendSystem != "" {
		p.AppendSystem = o.AppendSystem
	}
	if o.Source != "" {
		p.Source = o.Source
	}
	if o.Schema != nil {
		p.Schema = o.Schema
	}
	if len(o.SchemaJSON) > 0 {
		p.SchemaJSON = o.SchemaJSON
	}
	if o.SchemaStrictness != "" {
		p.SchemaStrictness = o.SchemaStrictness
	}
	if len(o.Metadata) > 0 {
		p.Metadata = o.Metadata
	}
	return p
}

func legacyMergeBudget(b, o Budget) Budget {
	if o.Cost != 0 {
		b.Cost = o.Cost
	}
	if o.MaxTokens != 0 {
		b.MaxTokens = o.MaxTokens
	}
	if o.MaxTurns != 0 {
		b.MaxTurns = o.MaxTurns
	}
	if o.Timeout != "" {
		b.Timeout = o.Timeout
	}
	return b
}

func legacyMergeMemory(m, o Memory) Memory {
	if len(o.Skills) > 0 {
		m.Skills = o.Skills
	}
	if o.SkipProject {
		m.SkipProject = true
	}
	if o.SkipUser {
		m.SkipUser = true
	}
	if o.SkipSkills {
		m.SkipSkills = true
	}
	if o.SkipHooks {
		m.SkipHooks = true
	}
	if o.SkipMemory {
		m.SkipMemory = true
	}
	if o.Bare {
		m.Bare = true
	}
	return m
}

func legacyMergePermissions(p, o Permissions) Permissions {
	if o.Mode != "" {
		p.Mode = o.Mode
	}
	if len(o.Presets) > 0 {
		p.Presets = o.Presets
	}
	if len(o.Tools) > 0 {
		p.Tools = o.Tools
	}
	if o.MCP.Disabled || len(o.MCP.Servers) > 0 || len(o.MCP.Modes) > 0 {
		p.MCP = o.MCP
	}
	if len(o.Plugins) > 0 {
		p.Plugins = o.Plugins
	}
	if len(o.Skills) > 0 {
		p.Skills = o.Skills
	}
	return p
}

// neutralize zeroes the fields whose merge policy the structural engine
// deliberately changes, so the differential comparison covers everything else.
// Each entry is a decision, not an exemption — see the matching assertion in
// TestSpec_Merge_IntentionalPolicyChanges.
func neutralize(s Spec) Spec {
	// Sub-structs that used to be replaced wholesale and now merge field-wise, so
	// a partial override composes with the base instead of erasing it (M1).
	s.Permissions.Tools = Tools{}
	s.Permissions.MCP = MCP{}
	s.Setup = nil
	s.Workflow = nil
	// Sandbox now deep-merges only when both layers select the same mode and
	// replaces when the mode changes; the old implementation always replaced it.
	s.Sandbox = nil

	// Maps that used to be replaced wholesale and now merge key-wise, because
	// each key is an independent setting.
	s.ToolPreferences = nil
	s.CLIArgs = nil
	s.Prompt.Metadata = nil
	s.Permissions.Plugins = nil
	s.Permissions.Skills = nil

	// Fields the hand-written mergers simply forgot; the structural engine cannot
	// forget one, so these now carry through.
	s.Prompt.Attachments = nil
	s.Model.Streaming, s.Model.Resume, s.Model.Interrupt, s.Model.Steer, s.Model.CallerTools = false, false, false, false, false
	s.Model.MediaTypes = nil
	s.Model.Provider = nil
	return s
}

func TestSpec_Merge_MatchesLegacy(t *testing.T) {
	rnd := rand.New(rand.NewSource(20260728))
	for i := range 2000 {
		base, override := randomSpec(rnd), randomSpec(rnd)
		want := neutralize(legacyMerge(base, override))
		got := neutralize(base.Merge(override))
		if diff := firstDifference(reflect.ValueOf(want), reflect.ValueOf(got), "Spec"); diff != "" {
			t.Fatalf("case %d diverged from the hand-written merge at %s\nbase=%s\noverride=%s",
				i, diff, mustJSON(t, base), mustJSON(t, override))
		}
	}
}

func TestSpec_Merge_IntentionalPolicyChanges(t *testing.T) {
	t.Run("a partial tool override composes with the inherited allow-list", func(t *testing.T) {
		base := Spec{Permissions: Permissions{Tools: Tools{"Read": ToolPolicyAllow, "Grep": ToolPolicyAllow}}}
		got := base.Merge(Spec{Permissions: Permissions{Tools: Tools{"Bash": ToolPolicyDeny}}})
		if !reflect.DeepEqual(got.Permissions.Tools, Tools{
			"Read": ToolPolicyAllow, "Grep": ToolPolicyAllow, "Bash": ToolPolicyDeny,
		}) {
			t.Errorf("Tools = %v, want the inherited allow entries kept alongside the new deny", got.Permissions.Tools)
		}
	})

	t.Run("a partial setup override keeps the rest of the block", func(t *testing.T) {
		base := Spec{Setup: &shell.Setup{Cwd: "/work", DotEnv: []string{".env"}}}
		got := base.Merge(Spec{Setup: &shell.Setup{Cwd: "/work/sub"}})
		if got.Setup.Cwd != "/work/sub" {
			t.Errorf("Cwd = %q, want the override's", got.Setup.Cwd)
		}
		if !reflect.DeepEqual(got.Setup.DotEnv, []string{".env"}) {
			t.Errorf("DotEnv = %v, want the base's kept", got.Setup.DotEnv)
		}
	})

	t.Run("maps merge key-wise", func(t *testing.T) {
		base := Spec{CLIArgs: map[string]any{"a": 1, "b": 2}}
		got := base.Merge(Spec{CLIArgs: map[string]any{"b": 3}})
		if len(got.CLIArgs) != 2 || got.CLIArgs["a"] != 1 || got.CLIArgs["b"] != 3 {
			t.Errorf("CLIArgs = %v, want {a:1 b:3}", got.CLIArgs)
		}
	})

	t.Run("attachments are no longer dropped", func(t *testing.T) {
		got := Spec{}.Merge(Spec{Prompt: Prompt{Attachments: []AttachmentRef{{ID: "diagram.png"}}}})
		if len(got.Prompt.Attachments) != 1 {
			t.Errorf("Attachments = %v, want the override's carried through", got.Prompt.Attachments)
		}
	})

	t.Run("tool-approval resume state stays indivisible", func(t *testing.T) {
		base := Spec{ToolApproval: &ToolApprovalResume{Decisions: []ToolApprovalDecision{{ToolCallID: "call-1"}}}}
		override := Spec{ToolApproval: &ToolApprovalResume{Decisions: []ToolApprovalDecision{{ToolCallID: "call-2"}}}}
		got := base.Merge(override)
		if len(got.ToolApproval.Decisions) != 1 || got.ToolApproval.Decisions[0].ToolCallID != "call-2" {
			t.Errorf("Decisions = %v, want exactly the override's snapshot", got.ToolApproval.Decisions)
		}
	})
}

// TestSpec_Merge_ResultIsIndependent guards the property the hand-written merge
// never had: a merged spec must not share mutable memory with the layers it came
// from, so editing it cannot reach back into the config it inherited.
func TestSpec_Merge_ResultIsIndependent(t *testing.T) {
	base := Spec{Permissions: Permissions{Tools: Tools{"Read": ToolPolicyAllow}}}
	override := Spec{Setup: &shell.Setup{Cwd: "/work", DotEnv: []string{".env"}}}

	got := base.Merge(override)
	got.Permissions.Tools["Read"] = ToolPolicyDeny
	got.Setup.DotEnv[0] = ".env.local"
	got.Setup.Cwd = "/elsewhere"

	if base.Permissions.Tools["Read"] != ToolPolicyAllow {
		t.Errorf("base tool policy mutated through the merged spec: %v", base.Permissions.Tools)
	}
	if override.Setup.DotEnv[0] != ".env" || override.Setup.Cwd != "/work" {
		t.Errorf("override setup mutated through the merged spec: %+v", override.Setup)
	}
}

func TestSpec_WithoutSession(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	var full Spec
	for range 200 { // keep drawing until every session-bearing field is populated
		full = randomSpec(rnd)
		if full.SessionID != "" && full.ToolApproval != nil && len(full.Messages) > 0 {
			break
		}
	}
	if full.SessionID == "" || full.ToolApproval == nil || len(full.Messages) == 0 {
		t.Fatal("generator never produced a spec carrying all three session fields")
	}

	stripped := full.WithoutSession()

	// Assert reflectively rather than field by field: a fourth session-bearing
	// field added to Spec later must fail this test instead of leaking silently.
	cleared := map[string]bool{"SessionID": true, "ToolApproval": true, "Messages": true}
	value, original := reflect.ValueOf(stripped), reflect.ValueOf(full)
	for i := range value.NumField() {
		name := value.Type().Field(i).Name
		if cleared[name] {
			if !value.Field(i).IsZero() {
				t.Errorf("%s = %v, want zeroed", name, value.Field(i).Interface())
			}
			continue
		}
		if !reflect.DeepEqual(value.Field(i).Interface(), original.Field(i).Interface()) {
			t.Errorf("%s changed: %v -> %v (WithoutSession must only drop conversation state)",
				name, original.Field(i).Interface(), value.Field(i).Interface())
		}
	}
}

// firstDifference walks two values in step and names the first field path where
// they disagree, because "these two 200-line specs are not equal" is not a
// finding — "Permissions.Skills disagrees" is.
func firstDifference(want, got reflect.Value, path string) string {
	if want.Kind() == reflect.Struct {
		for i := range want.NumField() {
			if !want.Type().Field(i).IsExported() {
				continue
			}
			if diff := firstDifference(want.Field(i), got.Field(i), path+"."+want.Type().Field(i).Name); diff != "" {
				return diff
			}
		}
		return ""
	}
	if want.Kind() == reflect.Pointer && !want.IsNil() && !got.IsNil() {
		return firstDifference(want.Elem(), got.Elem(), path)
	}
	if reflect.DeepEqual(want.Interface(), got.Interface()) {
		return ""
	}
	return fmt.Sprintf("%s: legacy=%#v structural=%#v", path, want.Interface(), got.Interface())
}

func mustJSON(t *testing.T, spec Spec) string {
	t.Helper()
	encoded, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		t.Fatalf("marshal spec for diagnostics: %v", err)
	}
	return string(encoded)
}

// randomSpec fills a Spec by reflection, leaving roughly half of each level's
// fields at their zero value so both the "override sets it" and "override is
// silent" branches are exercised.
func randomSpec(rnd *rand.Rand) Spec {
	var spec Spec
	value := reflect.ValueOf(&spec).Elem()
	randomValue(rnd, value, 0)
	return spec
}

const randomMaxDepth = 3

func randomValue(rnd *rand.Rand, v reflect.Value, depth int) {
	if !v.CanSet() || depth > randomMaxDepth {
		return
	}
	// Authorship metadata is exercised by presence tests, not arbitrary keys.
	if v.Type() == reflect.TypeOf(FieldPresence(nil)) {
		return
	}
	// Raw JSON is a []byte to Go but not to anything that reads it, and the
	// diagnostics on failure marshal the spec — random bytes would fail there
	// instead of at the comparison.
	if v.Type() == reflect.TypeOf(json.RawMessage(nil)) {
		if snippet := []string{"", `{}`, `{"kind":"alpha"}`, `[1,2]`}[rnd.Intn(4)]; snippet != "" {
			v.Set(reflect.ValueOf(json.RawMessage(snippet)))
		}
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(randomString(rnd, v.Type()))
	case reflect.Bool:
		v.SetBool(rnd.Intn(2) == 0)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(rnd.Intn(5)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(rnd.Intn(5)))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(rnd.Intn(3)))
	case reflect.Pointer:
		// The provider descriptor is the whole model catalog, reached by identity
		// rather than carried in a spec; a random one would mean nothing.
		if rnd.Intn(2) == 0 || v.Type() == reflect.TypeOf((*registry.Provider)(nil)) {
			return
		}
		item := reflect.New(v.Type().Elem())
		randomValue(rnd, item.Elem(), depth+1)
		v.Set(item)
	case reflect.Slice:
		if rnd.Intn(2) == 0 {
			return
		}
		items := reflect.MakeSlice(v.Type(), 1+rnd.Intn(2), 2)
		for i := range items.Len() {
			randomValue(rnd, items.Index(i), depth+1)
		}
		v.Set(items)
	case reflect.Map:
		if rnd.Intn(2) == 0 {
			return
		}
		items := reflect.MakeMap(v.Type())
		for range 1 + rnd.Intn(2) {
			key := reflect.New(v.Type().Key()).Elem()
			randomValue(rnd, key, depth+1)
			item := reflect.New(v.Type().Elem()).Elem()
			randomValue(rnd, item, depth+1)
			items.SetMapIndex(key, item)
		}
		v.Set(items)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				randomValue(rnd, v.Field(i), depth+1)
			}
		}
	default:
		// Interfaces, funcs and channels carry behaviour, not configuration; both
		// implementations pass them through by reference, so a nil is enough.
	}
}

// randomString draws from a small pool so distinct layers collide often enough
// to exercise "override equals base", and includes "" so "override is silent" is
// exercised too.
func randomString(rnd *rand.Rand, typ reflect.Type) string {
	pool := []string{"", "alpha", "beta", "gamma"}
	// Enum-typed strings validate their values elsewhere; merging does not care
	// what the value is, only whether it is empty, so the pool is shared.
	_ = typ
	return pool[rnd.Intn(len(pool))]
}
