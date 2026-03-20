package container

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func buildBaseImage() error {
	dir, err := os.MkdirTemp("", "captain-base-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	if err := writeBaseContext(dir); err != nil {
		return fmt.Errorf("writing base context: %w", err)
	}

	fmt.Printf("Building base image %s...\n", baseImageTag)
	cmd := exec.Command("docker", "build", "-t", baseImageTag, dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isImagePresent(tag string) bool {
	return exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", tag).Run() == nil
}
