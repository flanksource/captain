package cli

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	clickyapi "github.com/flanksource/clicky/api"

	"github.com/flanksource/captain/pkg/api"
)

// `captain permissions matrix` prints the declared permission capability table:
// which posture each runtime honours, which per-tool policies it can enforce and
// from which source, and which resources it can switch.
//
// It is a reporting command over static data — nothing is probed, nothing is run
// — and that is the point. The table is what makes a silent drop visible before a
// run is spent on it, and the golden fixture makes every later change to that
// truth an explicit diff in review rather than a behaviour change nobody noticed.

type PermissionsMatrixOptions struct {
	// Provider takes no shorthand: -p is provenance's here, and this command's
	// axis of interest is the policy source rather than the family.
	Provider   string `flag:"provider" help:"Restrict the matrix to one provider: anthropic|openai|google|deepseek"`
	Mode       string `flag:"mode" help:"Restrict the matrix to one runtime mode: api|agent|cli|cmux"`
	Provenance string `flag:"provenance" help:"Tool-policy source to show: agent, caller, or mcp" default:"agent" short:"p"`
	Notes      bool   `flag:"notes" help:"Print the caveat attached to each approximated or unsupported cell" short:"n"`
}

// PermissionsMatrixResult is the whole declaration. The JSON form is per-runtime
// because that is how a client consumes it; the pretty form transposes to
// settings × runtimes because that is how a human compares them.
type PermissionsMatrixResult struct {
	Provenance string                   `json:"provenance"`
	Runtimes   []PermissionsMatrixEntry `json:"runtimes"`
	Notes      []PermissionsMatrixNote  `json:"notes,omitempty"`
	legend     map[api.SupportKind]string
}

// PermissionsMatrixEntry is one runtime's row, carrying the served capability
// object verbatim so `--json` and the HTTP catalog cannot disagree.
type PermissionsMatrixEntry struct {
	Provider    string                     `json:"provider"`
	Mode        string                     `json:"mode"`
	Kind        string                     `json:"kind"`
	Permissions api.PermissionCapabilities `json:"permissions"`
}

// PermissionsMatrixNote is one caveat, addressed to a cell.
type PermissionsMatrixNote struct {
	Provider string `json:"provider" pretty:"label=Provider,table"`
	Mode     string `json:"mode" pretty:"label=Mode,table"`
	Setting string `json:"setting" pretty:"label=Setting,table"`
	Support string `json:"support" pretty:"label=Support,table"`
	Note    string `json:"note" pretty:"label=Note,table"`
}

func RunPermissionsMatrix(opts PermissionsMatrixOptions) (any, error) {
	provenance, err := parseProvenance(opts.Provenance)
	if err != nil {
		return nil, err
	}
	runtimes, err := matrixRuntimes(opts.Provider, opts.Mode)
	if err != nil {
		return nil, err
	}

	result := PermissionsMatrixResult{Provenance: string(provenance), legend: supportGlyphs()}
	for _, runtime := range runtimes {
		result.Runtimes = append(result.Runtimes, PermissionsMatrixEntry{
			Provider:    runtime.Provider,
			Mode:        string(runtime.Mode),
			Kind:        runtime.Mode.Kind(),
			Permissions: api.PermissionCapabilitiesFor(runtime),
		})
	}
	if opts.Notes {
		result.Notes = collectMatrixNotes(runtimes, provenance)
	}
	return result, nil
}

func parseProvenance(s string) (api.ToolProvenance, error) {
	want := api.ToolProvenance(strings.ToLower(strings.TrimSpace(s)))
	if want == "" {
		return api.ProvenanceAgent, nil
	}
	if slices.Contains(api.AllToolProvenances(), want) {
		return want, nil
	}
	names := make([]string, len(api.AllToolProvenances()))
	for i, p := range api.AllToolProvenances() {
		names[i] = string(p)
	}
	return "", fmt.Errorf("unknown tool provenance %q (valid: %s)", s, strings.Join(names, ", "))
}

// matrixRuntimes narrows the full matrix by the two independent axes. Neither is
// a runtime id: --provider names a family, --mode a mechanism, and passing both
// selects one cell.
func matrixRuntimes(providerSelector, modeSelector string) ([]api.Runtime, error) {
	provider := ""
	if raw := strings.TrimSpace(providerSelector); raw != "" {
		p, ok := api.ProviderByName(raw)
		if !ok {
			return nil, fmt.Errorf("unknown provider %q (valid: %s)", providerSelector, api.ProviderList())
		}
		provider = p.Name
	}
	var mode api.RuntimeMode
	if raw := strings.TrimSpace(modeSelector); raw != "" {
		parsed, ok := api.ParseRuntimeMode(raw)
		if !ok {
			return nil, fmt.Errorf("unknown mode %q (valid: %s)", modeSelector, api.RuntimeModeList())
		}
		mode = parsed
	}
	out := make([]api.Runtime, 0, len(api.AllRuntimes()))
	for _, runtime := range api.AllRuntimes() {
		if provider != "" && runtime.Provider != provider {
			continue
		}
		if mode != "" && runtime.Mode != mode {
			continue
		}
		out = append(out, runtime)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no runtime matches provider %q mode %q (available: %s)", providerSelector, modeSelector, api.RuntimeList())
	}
	return out, nil
}

// supportGlyphs keeps the four kinds distinguishable in a dense grid, using the
// ✓/✗/— vocabulary the rest of captain's output already speaks. A cell that is
// merely approximated must not look like a cell honoured exactly — that
// conflation is what let `dontAsk` read as supported on codex — so it gets its
// own mark rather than rounding to one of the two ends.
func supportGlyphs() map[api.SupportKind]string {
	return map[api.SupportKind]string{
		api.SupportNative:         "✓",
		api.SupportApproximated:   "~",
		api.SupportRequiresBroker: "?",
		api.SupportUnsupported:    "✗",
	}
}

func (r PermissionsMatrixResult) Pretty() clickyapi.Text {
	if len(r.Runtimes) == 0 {
		return clickyapi.Text{Content: "no runtimes"}
	}
	text := clickyapi.Text{}.
		Append("Declared permission capabilities", "font-bold").
		Append(fmt.Sprintf("  (tool policies for the %q source)", r.Provenance), "text-gray-500")

	// One table per family rather than one wide grid: every runtime name at once
	// does not fit a terminal, and truncating them collapses "anthropic cli" and
	// "anthropic cmux" into the same ambiguous header. Grouping also matches the
	// question people actually ask — "I picked claude, which transport honours
	// this?" — and the families come from RuntimeCatalog so the grouping is the
	// same one clients see.
	for _, family := range api.RuntimeCatalog() {
		entries := r.entriesIn(family)
		if len(entries) == 0 {
			continue
		}
		text = text.NewLine().NewLine().
			Append(family.Family, "font-bold text-blue-400").
			NewLine().
			Add(r.table(entries))
	}

	text = text.NewLine().
		Append("✓ honoured exactly   ~ approximated   ? needs an approval broker   ✗ not expressible", "text-gray-500")
	if len(r.Notes) > 0 {
		text = text.NewLine().NewLine().Append("Caveats", "font-bold").NewLine().Add(notesTable(r.Notes))
	}
	return text
}

// entriesIn selects this result's entries that belong to one family, in the
// family's own mode order.
func (r PermissionsMatrixResult) entriesIn(family api.RuntimeFamily) []PermissionsMatrixEntry {
	var out []PermissionsMatrixEntry
	for _, mode := range family.Modes {
		for _, entry := range r.Runtimes {
			if entry.Provider == family.Provider && entry.Mode == mode.Mode {
				out = append(out, entry)
			}
		}
	}
	return out
}

func (r PermissionsMatrixResult) table(entries []PermissionsMatrixEntry) clickyapi.TextTable {
	table := clickyapi.TextTable{
		Headers:    clickyapi.TextList{textCell("Setting")},
		FieldNames: []string{"setting"},
	}
	for i, entry := range entries {
		table.FieldNames = append(table.FieldNames, matrixColumnField(i))
		table.Headers = append(table.Headers, textCell(entry.Provider+" "+entry.Mode))
	}

	add := func(setting string, value func(api.PermissionCapabilities) string) {
		row := clickyapi.TableRow{"setting": cell(setting)}
		for i, entry := range entries {
			row[matrixColumnField(i)] = cell(value(entry.Permissions))
		}
		table.Rows = append(table.Rows, row)
	}
	for _, setting := range matrixSettings(api.ToolProvenance(r.Provenance)) {
		add(setting.label, func(c api.PermissionCapabilities) string {
			return r.legend[setting.support(c).Kind]
		})
	}
	add("built-in tools", func(c api.PermissionCapabilities) string {
		if len(c.Tools) == 0 {
			return "—"
		}
		return fmt.Sprintf("%d", len(c.Tools))
	})
	return table
}

// matrixSetting is one row of the matrix: a label and the cell it reads. Both
// the grid and the caveat list walk this, so a row can never appear in one with
// a label the other spells differently.
type matrixSetting struct {
	label   string
	support func(api.PermissionCapabilities) api.Support
}

func matrixSettings(provenance api.ToolProvenance) []matrixSetting {
	var out []matrixSetting
	for _, mode := range api.AllPermissionModes() {
		out = append(out, matrixSetting{"mode " + string(mode),
			func(c api.PermissionCapabilities) api.Support { return c.ModeSupport(mode) }})
	}
	for _, policy := range api.AllToolPolicies() {
		out = append(out, matrixSetting{"tool " + string(policy),
			func(c api.PermissionCapabilities) api.Support { return c.ToolPolicySupport(provenance, policy) }})
	}
	for _, kind := range api.AllResourceKinds() {
		for _, mode := range api.AllResourceModes() {
			out = append(out, matrixSetting{fmt.Sprintf("%s %s", kind, mode),
				func(c api.PermissionCapabilities) api.Support { return c.ResourceSupport(kind, mode) }})
		}
	}
	return out
}

func notesTable(notes []PermissionsMatrixNote) clickyapi.TextTable {
	table := clickyapi.TextTable{
		Headers:    clickyapi.TextList{textCell("Provider"), textCell("Mode"), textCell("Setting"), textCell("Support"), textCell("Note")},
		FieldNames: []string{"provider", "mode", "setting", "support", "note"},
	}
	for _, n := range notes {
		table.Rows = append(table.Rows, clickyapi.TableRow{
			"provider": cell(n.Provider), "mode": cell(n.Mode), "setting": cell(n.Setting),
			"support": cell(n.Support), "note": cell(n.Note),
		})
	}
	return table
}

// collectMatrixNotes gathers every explained cell. Sorting by runtime then
// setting keeps the golden fixture stable regardless of map iteration.
func collectMatrixNotes(runtimes []api.Runtime, provenance api.ToolProvenance) []PermissionsMatrixNote {
	var out []PermissionsMatrixNote
	for _, runtime := range runtimes {
		caps := api.PermissionCapabilitiesFor(runtime)
		for _, setting := range matrixSettings(provenance) {
			support := setting.support(caps)
			if support.Effects.Note == "" {
				continue
			}
			out = append(out, PermissionsMatrixNote{
				Provider: runtime.Provider, Mode: string(runtime.Mode), Setting: setting.label,
				Support: string(support.Kind), Note: support.Effects.Note,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if out[i].Mode != out[j].Mode {
			return out[i].Mode < out[j].Mode
		}
		return out[i].Setting < out[j].Setting
	})
	return out
}

func matrixColumnField(index int) string { return fmt.Sprintf("b%d", index+1) }
