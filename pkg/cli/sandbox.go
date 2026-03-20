package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/sandbox-runtime/sandbox"
)

type SandboxPresetsOptions struct{}

func (SandboxPresetsOptions) Help() api.Textable {
	return sandboxPresetsHelp()
}

func SandboxHelp() api.Text {
	text := clicky.Text("Sandbox configuration tools", "font-bold text-blue-400").NewLine().NewLine().
		AddText("Commands:", "font-bold text-blue-400").NewLine().
		AddText("  captain sandbox generate", "text-green-400").
		AddText("  — generate sandbox-runtime config", "text-gray-500").NewLine().
		AddText("  captain sandbox presets", "text-green-400").
		AddText("   — list available presets with details", "text-gray-500").NewLine().NewLine().
		AddText("See also:", "font-bold text-blue-400").NewLine().
		AddText("  captain container", "text-green-400").
		AddText("            — build container images with preset support", "text-gray-500").NewLine()
	return text
}

func RunSandboxPresets(_ SandboxPresetsOptions) (any, error) {
	return sandboxPresetsHelp(), nil
}

func sandboxPresetsHelp() api.Text {
	text := clicky.Text("Available presets", "font-bold text-blue-400").NewLine().NewLine()

	for _, name := range sandbox.ListPresets() {
		p, err := sandbox.GetPreset(name)
		if err != nil {
			text = text.AddText(fmt.Sprintf("  %s (error: %v)", name, err), "text-red-500").NewLine()
			continue
		}
		text = text.AddText("  "+name, "font-bold text-green-400").NewLine()
		if p.Env != nil {
			for k, v := range p.Env {
				text = text.AddText(fmt.Sprintf("    env:     %s", k), "text-cyan-400").
					AddText("="+v, "text-gray-400").NewLine()
			}
		}
		if len(p.PassthroughEnv) > 0 {
			text = text.AddText("    pass:    ", "text-cyan-400").
				AddText(strings.Join(p.PassthroughEnv, ", "), "text-gray-400").NewLine()
		}
		if p.Network != nil && len(p.Network.AllowedDomains) > 0 {
			text = text.AddText("    domains: ", "text-cyan-400").
				AddText(strings.Join(p.Network.AllowedDomains, ", "), "text-gray-400").NewLine()
		}
		if p.Network != nil && len(p.Network.AllowUnixSockets) > 0 {
			text = text.AddText("    sockets: ", "text-cyan-400").
				AddText(strings.Join(p.Network.AllowUnixSockets, ", "), "text-gray-400").NewLine()
		}
		if p.Filesystem != nil && len(p.Filesystem.AllowWrite) > 0 {
			text = text.AddText("    write:   ", "text-cyan-400").
				AddText(strings.Join(p.Filesystem.AllowWrite, ", "), "text-gray-400").NewLine()
		}
		text = text.NewLine()
	}

	text = text.AddText("Preset field mapping", "font-bold text-blue-400").
		AddText(" (sandbox vs container)", "text-gray-500").NewLine().NewLine()

	header := fmt.Sprintf("  %-35s %-25s %s", "Field", "Sandbox (srt)", "Container (docker)")
	sep := fmt.Sprintf("  %-35s %-25s %s", strings.Repeat("─", 35), strings.Repeat("─", 25), strings.Repeat("─", 25))
	text = text.AddText(header, "font-bold text-yellow-400").NewLine().
		AddText(sep, "text-gray-600").NewLine()

	rows := []struct{ field, sandbox, container string }{
		{"network.allowedDomains", "Proxy allow-list", "N/A"},
		{"network.allowUnixSockets", "Socket access", "Volume mount"},
		{"filesystem.allowWrite", "Writable paths", "Named volumes"},
		{"env", "Set env vars", "-e KEY=VALUE"},
		{"passthroughEnv", "Forward host env", "-e KEY=$KEY"},
	}
	for _, r := range rows {
		text = text.AddText(fmt.Sprintf("  %-35s ", r.field), "text-cyan-400").
			AddText(fmt.Sprintf("%-25s ", r.sandbox), "text-gray-300").
			AddText(r.container, "text-gray-400").NewLine()
	}

	text = text.NewLine().
		AddText("Usage:", "font-bold text-blue-400").NewLine().
		AddText("  captain container generate --preset golang", "text-green-400").
		AddText("   # all components, golang env/cache", "text-gray-500").NewLine().
		AddText("  captain container generate --preset golang,npm", "text-green-400").
		AddText("  # combine presets", "text-gray-500").NewLine().
		AddText("  captain container generate", "text-green-400").
		AddText("                    # all components, no presets", "text-gray-500").NewLine()

	return text
}
