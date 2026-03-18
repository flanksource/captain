package container

import (
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
