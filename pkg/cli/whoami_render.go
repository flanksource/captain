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
		t = t.NewLine().Add(a.prettyLine(r.showModels, r.sampleLimit))
	}
	return t
}

func (a AdapterStatus) prettyLine(showModels bool, limit int) api.Text {
	t := api.Text{}.
		Append("  ", "").
		Add(a.statusIcon()).Space().
		Append(fmt.Sprintf("%-13s", a.Backend), "font-medium")

	t = a.appendAuth(t)
	if a.Type == "cli" {
		t = a.appendBinary(t)
	}
	if showModels {
		t = a.appendModels(t, limit)
	}
	return t
}

// statusIcon is a green check when the adapter can run, a yellow warning when it
// is authenticated but unusable (CLI binary missing), and a red cross otherwise.
func (a AdapterStatus) statusIcon() api.Textable {
	switch {
	case a.Ready():
		return icons.Check
	case a.Authenticated:
		return icons.Warning
	default:
		return icons.Cross
	}
}

func (a AdapterStatus) appendAuth(t api.Text) api.Text {
	if !a.Authenticated {
		msg := "not configured"
		if vars := strings.Join(ai.AuthEnvVars(ai.Backend(a.Backend)), " or "); vars != "" {
			msg += " (set " + vars + ")"
		}
		return t.Append(msg, "text-red-500")
	}
	t = t.Append(a.AuthMethod, "text-green-700")
	if a.AuthDetail != "" {
		t = t.Append("  ", "").Add(icons.Key).Space().Append(a.AuthDetail, "text-gray-500")
	}
	return t
}

func (a AdapterStatus) appendBinary(t api.Text) api.Text {
	if a.Binary != "" {
		return t.Append("  ", "").Append(a.Binary, "text-gray-400 italic")
	}
	return t.Append("  ", "").Append(a.BinaryMissing+" not in PATH", "text-amber-600")
}

func (a AdapterStatus) appendModels(t api.Text, limit int) api.Text {
	if a.ModelError != "" {
		return t.NewLine().Append("      ", "").Add(icons.Info).Space().
			Append(a.ModelError, "text-gray-500 italic")
	}
	if a.ModelCount == 0 {
		return t
	}

	t = t.Append("  ", "").
		Append(fmt.Sprintf("%d models", a.ModelCount), "text-blue-600 font-medium")

	sample := a.Models
	if limit > 0 && len(sample) > limit {
		sample = sample[:limit]
	}
	if len(sample) > 0 {
		line := strings.Join(sample, ", ")
		if len(sample) < a.ModelCount {
			line += fmt.Sprintf(", … (+%d more)", a.ModelCount-len(sample))
		}
		t = t.NewLine().Append("      ", "").Append(line, "text-gray-500")
	}
	return t
}
