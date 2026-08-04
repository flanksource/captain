package adapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/container"
)

// containerSandbox runs the agent CLI inside a container image built by
// `captain container` (pkg/container). It wraps the argv with an ephemeral
// `docker run` assembled from the same configuration the generated sbx-*
// scripts use: the project's .container-sandbox.yaml, overridable by the
// backend's options in ~/.captain.yaml.
type containerSandbox struct {
	options map[string]any

	cwd string
	cfg container.SandboxConfig
}

// Container is the SandboxFactory for the container adapter.
func Container(cfg api.SandboxConfig) (api.Sandbox, error) {
	return &containerSandbox{options: cfg.Options}, nil
}

func init() { api.RegisterSandbox(api.SandboxContainer, Container) }

func (c *containerSandbox) Kind() api.SandboxKind { return api.SandboxContainer }

func (c *containerSandbox) Prepare(_ context.Context, spec *api.Spec) (*api.SandboxSession, error) {
	c.cwd = spec.Cwd()
	if c.cwd == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve container working directory: %w", err)
		}
		c.cwd = cwd
	}

	// The project's generated config is the base; backend options override it.
	configPath := filepath.Join(c.cwd, ".container-sandbox.yaml")
	if _, err := os.Stat(configPath); err == nil {
		cfg, err := container.LoadSandboxConfig(configPath)
		if err != nil {
			return nil, err
		}
		c.cfg = cfg
	}
	if image, ok := c.options["image"].(string); ok && image != "" {
		c.cfg.Image = image
	}
	if presets, ok := c.options["presets"].([]any); ok {
		c.cfg.Presets = nil
		for _, preset := range presets {
			if name, ok := preset.(string); ok {
				c.cfg.Presets = append(c.cfg.Presets, name)
			}
		}
	}
	return &api.SandboxSession{}, nil
}

func (c *containerSandbox) Wrap(_ context.Context, command string, args, env []string) (string, []string, []string, error) {
	if c.cfg.Image == "" {
		return "", nil, nil, fmt.Errorf(
			"container sandbox has no image: run `captain container` in %s to generate .container-sandbox.yaml, or set image on the sandbox backend", c.cwd)
	}

	dockerArgs := []string{"run", "--rm", "-i", "-w", c.cwd, "-v", c.cwd + ":" + c.cwd}

	// Credential env rides by name (-e KEY), so the value comes from the docker
	// client's environment and never appears in the argv.
	for _, name := range append(cliCredentialEnv(command), c.cfg.EnvPassthrough...) {
		if os.Getenv(name) != "" {
			dockerArgs = append(dockerArgs, "-e", name)
		}
	}
	for key, value := range c.cfg.Env {
		dockerArgs = append(dockerArgs, "-e", key+"="+os.ExpandEnv(value))
	}

	home := container.HostUser{Username: c.cfg.User.Username, UID: c.cfg.User.UID, GID: c.cfg.User.GID}.ContainerHome()
	presetEnv, presetVolumes := container.ResolveSandboxEnv(c.cfg.Presets, home)
	for _, item := range presetEnv {
		dockerArgs = append(dockerArgs, "-e", item)
	}
	for _, volume := range append(presetVolumes, container.ResolveDependencyVolumes(c.cfg.Presets, c.cwd, home)...) {
		dockerArgs = append(dockerArgs, "-v", volume.Source+":"+volume.Target)
	}
	for _, volume := range c.cfg.Volumes {
		mount := volume.Source + ":" + volume.Target
		if volume.ReadOnly {
			mount += ":ro"
		}
		dockerArgs = append(dockerArgs, "-v", mount)
	}

	dockerArgs = append(dockerArgs, c.cfg.Image)
	dockerArgs = append(dockerArgs, command)
	dockerArgs = append(dockerArgs, args...)
	return "docker", dockerArgs, env, nil
}

func (c *containerSandbox) Close() error { return nil }
