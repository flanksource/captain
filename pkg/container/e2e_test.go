//go:build e2e

package container

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// requireDocker skips the test if docker is unavailable or claude-env:base is missing.
func requireDocker(t *testing.T) {
	t.Helper()
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("docker not available")
	}
	if !isImagePresent("claude-env:base") {
		t.Skip("claude-env:base not present; build it first with 'captain container build'")
	}
}

func buildTestImage(t *testing.T, tag string, install []string) string {
	t.Helper()
	outDir := t.TempDir()
	user := DetectHostUser()

	agentSrc := outDir + "/test.md"
	if err := os.WriteFile(agentSrc, []byte("# test"), 0o644); err != nil {
		t.Fatalf("writing stub agent: %v", err)
	}

	components := []Component{{
		Category:   CategoryAgents,
		Name:       "test",
		SourcePath: agentSrc,
		TargetPath: user.ContainerHome() + "/.claude/agents/test.md",
		Selected:   true,
	}}

	contextDir, err := Generate(GenerateInput{
		Name:          tag,
		BaseImage:     "claude-env:base",
		Mode:          ModeCopy,
		Components:    components,
		OutputDir:     outDir,
		User:          user,
		PresetInstall: install,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if err := Build(BuildInput{Tag: tag, ContextDir: contextDir, User: user}); err != nil {
		t.Fatalf("Build %s: %v", tag, err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	return tag
}

func runInImage(t *testing.T, image string, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", append([]string{"run", "--rm", image}, args...)...).Output()
	if err != nil {
		combined, _ := exec.Command("docker", append([]string{"run", "--rm", image}, args...)...).CombinedOutput()
		t.Fatalf("docker run %s %v: %v\n%s", image, args, err, combined)
	}
	return strings.TrimSpace(string(out))
}

// TestE2EPresetInstallPython builds an image with the python3 preset install
// snippet and verifies python3 is callable inside the container.
func TestE2EPresetInstallPython(t *testing.T) {
	requireDocker(t)

	tag := buildTestImage(t, "captain-e2e-test:python", []string{
		"apt-get update -qq && apt-get install -y --no-install-recommends python3 && apt-get clean && rm -rf /var/lib/apt/lists/*",
	})

	version := runInImage(t, tag, "python3", "--version")
	if !strings.HasPrefix(version, "Python 3") {
		t.Errorf("expected Python 3.x, got %q", version)
	}
}

// TestE2EEnsureBaseImageNoOp verifies EnsureBaseImage is a no-op when the
// base image already exists.
func TestE2EEnsureBaseImageNoOp(t *testing.T) {
	requireDocker(t)
	if err := EnsureBaseImage("claude-env:base"); err != nil {
		t.Errorf("EnsureBaseImage returned error when image is present: %v", err)
	}
}

// TestE2EUserDroppedByEntrypoint verifies the gosu entrypoint drops the
// process from root to the host user inside the container.
func TestE2EUserDroppedByEntrypoint(t *testing.T) {
	requireDocker(t)

	// Skip if the base image predates gosu entrypoint support.
	out, _ := exec.Command("docker", "inspect", "claude-env:base", "--format", "{{.Config.Entrypoint}}").Output()
	if strings.TrimSpace(string(out)) == "[]" {
		t.Skip("claude-env:base has no entrypoint; rebuild base image to test gosu user-drop")
	}

	user := DetectHostUser()
	tag := buildTestImage(t, "captain-e2e-test:user-root", nil)

	whoami := runInImage(t, tag, "whoami")
	if whoami == "root" {
		t.Errorf("entrypoint should drop from root to %q via gosu, but got root", user.Username)
	}
	if whoami != user.Username {
		t.Errorf("expected whoami=%q, got %q", user.Username, whoami)
	}
}
