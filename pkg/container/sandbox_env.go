package container

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/sandbox/presets"
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

func depVolumeName(pwd, depPath string) string {
	h := sha256.Sum256([]byte(pwd))
	hash8 := hex.EncodeToString(h[:4])
	base := filepath.Base(depPath)
	safe := strings.TrimLeft(base, ".")
	if safe == "" {
		safe = "dep"
	}
	return fmt.Sprintf("dep-%s-%s", hash8, safe)
}

func ResolveDependencyVolumes(presetNames []string, pwd, containerHome string) []Volume {
	dirs := presets.GetDependencyDirs(presetNames)
	if len(dirs) == 0 {
		return nil
	}

	cacheTargets := make(map[string]bool)
	for _, cm := range cacheMappings {
		target := strings.ReplaceAll(cm.mountTarget, "{home}", containerHome)
		cacheTargets[target] = true
	}

	var volumes []Volume
	for _, dir := range dirs {
		expanded := os.ExpandEnv(dir)
		var target string
		if filepath.IsAbs(expanded) {
			home := os.Getenv("HOME")
			if home != "" && strings.HasPrefix(expanded, home) {
				target = containerHome + expanded[len(home):]
			} else {
				target = expanded
			}
		} else {
			target = filepath.Join(pwd, expanded)
		}

		if cacheTargets[target] {
			continue
		}

		volumes = append(volumes, Volume{
			Source: depVolumeName(pwd, dir),
			Target: target,
		})
	}
	return volumes
}
