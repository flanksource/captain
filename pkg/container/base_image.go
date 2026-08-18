package container

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flanksource/clicky"
)

//go:embed base/Dockerfile
var baseDockerfileContent []byte

//go:embed base/entrypoint.sh
var baseEntrypointContent []byte

const baseImageTag = "claude-env:base"

func EnsureBaseImage(baseImage string) error {
	if baseImage != baseImageTag || isImagePresent(baseImageTag) {
		return nil
	}
	return buildBaseImage()
}

func writeBaseContext(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), baseDockerfileContent, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "entrypoint.sh"), baseEntrypointContent, 0o755)
}

// githubTokenSecretArgs passes the host's GitHub token to `deps` as a BuildKit
// secret so the image can resolve release assets without hitting the 60 req/hr
// unauthenticated rate limit. The token is mounted, never baked into a layer or
// image history. The Dockerfile declares the mount as required=false, so an
// unauthenticated build still works.
func githubTokenSecretArgs() ([]string, []string) {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		token := os.Getenv(name)
		if token == "" {
			continue
		}
		return []string{"--secret", "id=GITHUB_TOKEN,env=GITHUB_TOKEN"},
			[]string{"GITHUB_TOKEN=" + token}
	}
	return nil, nil
}

// buildKitEnv returns the environment for a `docker build`, forcing BuildKit so
// secret mounts are honoured on daemons that still default to the legacy builder.
func buildKitEnv(extra []string) []string {
	env := append(os.Environ(), "DOCKER_BUILDKIT=1")
	return append(env, extra...)
}

func buildBaseImage() error {
	dir, err := os.MkdirTemp("", "captain-base-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	if err := writeBaseContext(dir); err != nil {
		return fmt.Errorf("writing base context: %w", err)
	}

	secretArgs, secretEnv := githubTokenSecretArgs()

	clicky.Printf("Building base image %s...\n", baseImageTag)
	args := append([]string{"build", "-t", baseImageTag}, secretArgs...)
	cmd := exec.Command("docker", append(args, dir)...)
	cmd.Env = buildKitEnv(secretEnv)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isImagePresent(tag string) bool {
	return exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", tag).Run() == nil
}
