package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/sandbox-runtime/sandbox"
)

type SandboxPresetsOptions struct{}

func RunSandboxPresets(_ SandboxPresetsOptions) (any, error) {
	presets := sandbox.ListPresets()
	fmt.Println("Available presets:")
	fmt.Println()
	for _, name := range presets {
		p, err := sandbox.GetPreset(name)
		if err != nil {
			fmt.Printf("  %s (error: %v)\n", name, err)
			continue
		}
		fmt.Printf("  %s\n", name)
		if p.Env != nil {
			for k, v := range p.Env {
				fmt.Printf("    env:     %s=%s\n", k, v)
			}
		}
		if len(p.PassthroughEnv) > 0 {
			fmt.Printf("    pass:    %s\n", strings.Join(p.PassthroughEnv, ", "))
		}
		if p.Network != nil && len(p.Network.AllowedDomains) > 0 {
			fmt.Printf("    domains: %s\n", strings.Join(p.Network.AllowedDomains, ", "))
		}
		if p.Network != nil && len(p.Network.AllowUnixSockets) > 0 {
			fmt.Printf("    sockets: %s\n", strings.Join(p.Network.AllowUnixSockets, ", "))
		}
		if p.Filesystem != nil && len(p.Filesystem.AllowWrite) > 0 {
			fmt.Printf("    write:   %s\n", strings.Join(p.Filesystem.AllowWrite, ", "))
		}
		fmt.Println()
	}

	fmt.Println("Preset field mapping (sandbox vs container):")
	fmt.Println()
	fmt.Printf("  %-35s %-25s %s\n", "Field", "Sandbox (srt)", "Container (docker)")
	fmt.Printf("  %-35s %-25s %s\n", strings.Repeat("-", 35), strings.Repeat("-", 25), strings.Repeat("-", 25))
	fmt.Printf("  %-35s %-25s %s\n", "network.allowedDomains", "Proxy allow-list", "N/A")
	fmt.Printf("  %-35s %-25s %s\n", "network.allowUnixSockets", "Socket access", "Volume mount")
	fmt.Printf("  %-35s %-25s %s\n", "filesystem.allowWrite", "Writable paths", "Named volumes")
	fmt.Printf("  %-35s %-25s %s\n", "env", "Set env vars", "-e KEY=VALUE")
	fmt.Printf("  %-35s %-25s %s\n", "passthroughEnv", "Forward host env", "-e KEY=$KEY")
	return nil, nil
}
