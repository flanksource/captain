package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// Pretty renders the adapters grouped into API providers and CLI agents, each as
// a status line with its auth method and (when probed) model count.
func (r WhoamiResult) Pretty() api.Text {
	t := api.Text{}.
		Add(icons.AI).Space().
		Append("AI Adapters", "font-bold text-blue-600")

	t = r.renderGroup(t, "api", icons.Cloud, "API providers")
	t = r.renderGroup(t, "cli", icons.Terminal, "CLI agents")
	return t
}

func (r WhoamiResult) renderGroup(t api.Text, kind string, icon api.Textable, title string) api.Text {
	var rows []AdapterStatus
	for _, a := range r.Adapters {
		if a.Type == kind {
			rows = append(rows, a)
		}
	}
	if len(rows) == 0 {
		return t
	}

	t = t.NewLine().NewLine().
		Add(icon).Space().
		Append(title, "font-bold text-gray-700")
	for _, a := range rows {
		t = t.NewLine().Add(adapterPrettyLine(a, r.showModels, r.sampleLimit))
	}
	return t
}

// AdapterStatus is defined in pkg/ai, so its renderers are free functions here
// rather than methods (a package may only declare methods on its own types).

func adapterPrettyLine(a AdapterStatus, showModels bool, limit int) api.Text {
	t := api.Text{}.
		Append("  ", "").
		Add(adapterStatusIcon(a)).Space().
		Append(fmt.Sprintf("%-13s", a.Provider+" "+a.Mode), "font-medium")

	t = adapterAppendAuth(a, t)
	if a.Type == "cli" {
		t = adapterAppendBinary(a, t)
	}
	if a.Disabled {
		t = t.Append("  ", "").Append("disabled ("+a.DisabledReason+")", "text-gray-400 italic")
	}
	if showModels {
		t = adapterAppendModels(a, t, limit)
	}
	return t
}

// adapterStatusIcon is a green check when the adapter can run, a yellow warning
// when it is authenticated but unusable (CLI binary missing or switched off),
// and a red cross otherwise.
func adapterStatusIcon(a AdapterStatus) api.Textable {
	switch {
	case a.Disabled:
		return icons.Warning
	case a.Ready():
		return icons.Check
	case a.Authenticated:
		return icons.Warning
	default:
		return icons.Cross
	}
}

func adapterAppendAuth(a AdapterStatus, t api.Text) api.Text {
	if !a.Authenticated {
		msg := "not configured"
		if p, ok := ai.ProviderByName(a.Provider); ok {
			if vars := strings.Join(ai.AuthEnvVars(p, ai.RuntimeMode(a.Mode)), " or "); vars != "" {
				msg += " (set " + vars + ")"
			}
		}
		return t.Append(msg, "text-red-500")
	}
	t = t.Append(a.AuthMethod, "text-green-700")
	if a.AuthDetail != "" {
		t = t.Append("  ", "").Add(icons.Key).Space().Append(a.AuthDetail, "text-gray-500")
	}
	return t
}

func adapterAppendBinary(a AdapterStatus, t api.Text) api.Text {
	if a.Binary != "" {
		return t.Append("  ", "").Append(a.Binary, "text-gray-400 italic")
	}
	return t.Append("  ", "").Append(a.BinaryMissing+" not in PATH", "text-amber-600")
}

func adapterAppendModels(a AdapterStatus, t api.Text, limit int) api.Text {
	if a.ModelError != "" {
		return t.NewLine().Append("      ", "").Add(icons.Info).Space().
			Append(a.ModelError, "text-gray-500 italic")
	}
	if a.ModelCount == 0 {
		return t
	}

	t = t.Append("  ", "").
		Append(fmt.Sprintf("%d models", a.ModelCount), "text-blue-600 font-medium")

	sample := a.ModelDetails
	if len(sample) == 0 && len(a.Models) > 0 {
		sample = make([]ai.ModelDef, 0, len(a.Models))
		for _, id := range a.Models {
			p, _ := ai.ProviderByName(a.Provider)
			sample = append(sample, ai.ModelDef{ID: id, ReleaseDate: ai.CatalogReleaseDate(p, ai.RuntimeMode(a.Mode), id)})
		}
	}
	if limit > 0 && len(sample) > limit {
		sample = sample[:limit]
	}
	for _, model := range sample {
		label := model.ID
		if model.ReleaseDate != "" {
			label += " (" + model.ReleaseDate + ")"
		}
		if len(model.SupportedEfforts) > 0 {
			efforts := make([]string, 0, len(model.SupportedEfforts))
			for _, effort := range model.SupportedEfforts {
				efforts = append(efforts, string(effort))
			}
			label += " [effort: " + strings.Join(efforts, "|") + "]"
		}
		if model.Disabled {
			label += " [disabled]"
		}
		styles := "text-gray-500"
		if model.Disabled {
			styles += " line-through"
		}
		t = t.NewLine().Append("      - ", "").Append(label, styles)
	}
	if len(sample) < a.ModelCount {
		t = t.NewLine().Append("      ", "").Append(fmt.Sprintf("... (+%d more)", a.ModelCount-len(sample)), "text-gray-500")
	}
	return t
}
