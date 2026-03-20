package container

import (
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/sandbox-runtime/sandbox"
)

type cacheMapping struct {
	pathPatterns []string
	volumeName   string
	mountTarget  string // uses {home} placeholder
}

var cacheMappings = []cacheMapping{
	{pathPatterns: []string{"$GOPATH", "$GOMODCACHE"}, volumeName: "cache-gomod", mountTarget: "/go/pkg/mod"},
	{pathPatterns: []string{"$HOME/.npm"}, volumeName: "cache-npm", mountTarget: "{home}/.npm"},
	{pathPatterns: []string{"$HOME/.cache/pip"}, volumeName: "cache-pip", mountTarget: "{home}/.cache/pip"},
	{pathPatterns: []string{"$HOME/.cargo"}, volumeName: "cache-cargo", mountTarget: "{home}/.cargo"},
	{pathPatterns: []string{"$HOME/.cache/ms-playwright"}, volumeName: "cache-playwright", mountTarget: "{home}/.cache/ms-playwright"},
}

func ResolveSandboxEnv(presetNames []string, containerHome string) (envVars []string, volumes []Volume) {
	if len(presetNames) == 0 {
		return nil, nil
	}

	var profiles []*sandbox.Profile
	for _, name := range presetNames {
		p, err := sandbox.GetPreset(name)
		if err != nil {
			continue
		}
		profiles = append(profiles, p)
	}
	if len(profiles) == 0 {
		return nil, nil
	}

	merged := sandbox.MergeProfiles(profiles...)
	cfg, err := sandbox.ResolveProfile(merged)
	if err != nil {
		return nil, nil
	}

	for _, name := range cfg.PassthroughEnv {
		if val := os.Getenv(name); val != "" {
			envVars = append(envVars, fmt.Sprintf("%s=%s", name, val))
		}
	}

	for k, v := range cfg.Env {
		envVars = append(envVars, fmt.Sprintf("%s=%s", k, os.ExpandEnv(v)))
	}

	writePaths := make(map[string]bool)
	if cfg.Filesystem.AllowWrite != nil {
		for _, p := range cfg.Filesystem.AllowWrite {
			writePaths[p] = true
		}
	}

	for _, cm := range cacheMappings {
		for _, pattern := range cm.pathPatterns {
			expanded := os.ExpandEnv(pattern)
			if writePaths[pattern] || writePaths[expanded] {
				target := strings.ReplaceAll(cm.mountTarget, "{home}", containerHome)
				volumes = append(volumes, Volume{
					Source: cm.volumeName,
					Target: target,
				})
				break
			}
		}
	}

	if cfg.Network.AllowUnixSockets != nil {
		for _, sock := range cfg.Network.AllowUnixSockets {
			volumes = append(volumes, Volume{Source: sock, Target: sock})
		}
	}

	return envVars, volumes
}
