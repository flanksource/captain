package container

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flanksource/clicky"
)

type BuildInput struct {
	ContextDir string
	Tag        string
	User       HostUser
}

func Build(input BuildInput) error {
	if input.Tag == "" {
		input.Tag = "claude-env:custom"
	}
	if input.ContextDir == "" {
		pwd, _ := os.Getwd()
		input.ContextDir = filepath.Join(pwd, ".container-sandbox")
	}
	if input.User.Username == "" {
		input.User = DetectHostUser()
	}

	cmd := exec.Command("docker", "build",
		"-t", input.Tag,
		"--build-arg", fmt.Sprintf("USERNAME=%s", input.User.Username),
		"--build-arg", fmt.Sprintf("USER_UID=%d", input.User.UID),
		"--build-arg", fmt.Sprintf("USER_GID=%d", input.User.GID),
		"-f", filepath.Join(input.ContextDir, "Dockerfile"),
		input.ContextDir,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func PrintBuildInstructions(tag string) {
	clicky.Println()
	clicky.Println("Build complete!")
	clicky.Printf("  Image: %s\n", tag)
}
