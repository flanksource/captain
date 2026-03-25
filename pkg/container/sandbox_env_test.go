package container

import (
	"strings"
	"testing"
)

func TestResolveSandboxEnvGolang(t *testing.T) {
	t.Setenv("GOPATH", "/home/user/go")
	t.Setenv("GOMODCACHE", "/home/user/go/pkg/mod")

	envVars, volumes := ResolveSandboxEnv([]string{"golang"}, "/home/testuser")

	foundGOPATH := false
	for _, e := range envVars {
		if e == "GOPATH=/home/user/go" {
			foundGOPATH = true
		}
	}
	if !foundGOPATH {
		t.Errorf("expected GOPATH passthrough, got %v", envVars)
	}

	foundGoModVol := false
	for _, v := range volumes {
		if v.Source == "cache-gomod" && v.Target == "/go/pkg/mod" {
			foundGoModVol = true
		}
	}
	if !foundGoModVol {
		t.Errorf("expected cache-gomod volume, got %v", volumes)
	}
}

func TestResolveSandboxEnvNpm(t *testing.T) {
	t.Setenv("HOME", "/home/user")

	envVars, volumes := ResolveSandboxEnv([]string{"npm"}, "/home/testuser")

	foundNodeTLS := false
	for _, e := range envVars {
		if e == "NODE_TLS_REJECT_UNAUTHORIZED=0" {
			foundNodeTLS = true
		}
	}
	if !foundNodeTLS {
		t.Errorf("expected NODE_TLS_REJECT_UNAUTHORIZED env var, got %v", envVars)
	}

	foundNpmVol := false
	for _, v := range volumes {
		if v.Source == "cache-npm" && v.Target == "/home/testuser/.npm" {
			foundNpmVol = true
		}
	}
	if !foundNpmVol {
		t.Errorf("expected cache-npm volume, got %v", volumes)
	}
}

func TestResolveSandboxEnvEmpty(t *testing.T) {
	envVars, volumes := ResolveSandboxEnv(nil, "/home/testuser")
	if len(envVars) != 0 || len(volumes) != 0 {
		t.Errorf("expected empty results for nil presets, got envVars=%v volumes=%v", envVars, volumes)
	}
}

func TestResolveSandboxEnvUnknownPreset(t *testing.T) {
	envVars, volumes := ResolveSandboxEnv([]string{"nonexistent"}, "/home/testuser")
	if len(envVars) != 0 || len(volumes) != 0 {
		t.Errorf("expected empty results for unknown preset, got envVars=%v volumes=%v", envVars, volumes)
	}
}

func TestDepVolumeName(t *testing.T) {
	name1 := depVolumeName("/project/a", "node_modules")
	name2 := depVolumeName("/project/b", "node_modules")
	if name1 == name2 {
		t.Error("different PWDs should produce different volume names")
	}
	if !strings.HasPrefix(name1, "dep-") || !strings.HasSuffix(name1, "-node_modules") {
		t.Errorf("unexpected volume name format: %s", name1)
	}
	if depVolumeName("/project/a", "node_modules") != name1 {
		t.Error("volume name should be deterministic")
	}
	dotName := depVolumeName("/project/a", ".next")
	if !strings.HasSuffix(dotName, "-next") {
		t.Errorf("unexpected dot-prefixed volume name: %s", dotName)
	}
}

func TestResolveDependencyVolumesNpm(t *testing.T) {
	pwd := "/workspace/myproject"
	volumes := ResolveDependencyVolumes([]string{"npm"}, pwd, "/home/testuser")
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d: %v", len(volumes), volumes)
	}
	if volumes[0].Target != pwd+"/node_modules" {
		t.Errorf("expected target %s/node_modules, got %s", pwd, volumes[0].Target)
	}
	if !strings.HasPrefix(volumes[0].Source, "dep-") {
		t.Errorf("expected dep- prefixed source, got %s", volumes[0].Source)
	}
}

func TestResolveDependencyVolumesAbsPath(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	pwd := "/workspace/myproject"
	volumes := ResolveDependencyVolumes([]string{"golang"}, pwd, "/home/containeruser")
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume for golang deps, got %d: %v", len(volumes), volumes)
	}
	if volumes[0].Target != "/home/containeruser/.cache/go-build" {
		t.Errorf("expected container home path, got %s", volumes[0].Target)
	}
}

func TestResolveDependencyVolumesEmpty(t *testing.T) {
	if v := ResolveDependencyVolumes(nil, "/pwd", "/home"); v != nil {
		t.Errorf("expected nil, got %v", v)
	}
	if v := ResolveDependencyVolumes([]string{"nonexistent"}, "/pwd", "/home"); v != nil {
		t.Errorf("expected nil for unknown, got %v", v)
	}
}
