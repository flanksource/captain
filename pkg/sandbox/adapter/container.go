package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/container"
)

// containerSandbox runs the agent CLI inside a container image built by
// `captain container` (pkg/container). It wraps the argv with an ephemeral
// `docker run` assembled from two sources with different trust levels:
//
//   - The backend's options in ~/.captain.yaml — the USER's own machine
//     config, trusted: image, presets, env, envPassthrough, volumes.
//   - The project's .container-sandbox.yaml — REPOSITORY content, untrusted:
//     a cloned repo must not be able to reach the host through it. Its image,
//     and cwd-contained volumes are honoured; presets, ambient env and outside
//     volumes are refused loudly, naming the trusted place to declare them.
type containerSandbox struct {
	options map[string]any

	cwd string
	cfg container.SandboxConfig

	optionEnv         map[string]string
	optionPassthrough []string
	optionVolumes     []string
}

// Container is the SandboxFactory for the container adapter.
func Container(cfg api.SandboxConfig) (api.Sandbox, error) {
	return &containerSandbox{options: cfg.Options}, nil
}

func init() { api.RegisterSandbox(api.SandboxDocker, Container) }

func (c *containerSandbox) Kind() api.SandboxKind { return api.SandboxDocker }

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
		if err := rejectUntrustedContainerConfig(cfg, c.cwd); err != nil {
			return nil, err
		}
		// Normalize relative volume sources onto the project directory: docker
		// would otherwise treat them as named volumes, mounting something other
		// than the path the containment check above approved.
		for i, volume := range cfg.Volumes {
			if !filepath.IsAbs(volume.Source) {
				cfg.Volumes[i].Source = filepath.Join(c.cwd, volume.Source)
			}
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
	if err := c.loadTrustedOptions(); err != nil {
		return nil, err
	}
	return &api.SandboxSession{}, nil
}

// rejectUntrustedContainerConfig refuses the repository-supplied settings that
// would reach the host: ambient environment access, out-of-project mounts, and
// presets — whose expansion mounts host cache directories and passes
// credential env through. Refusing beats filtering — a cloned repo silently
// granting itself less than it asked for still granted itself something the
// user never saw.
func rejectUntrustedContainerConfig(cfg container.SandboxConfig, cwd string) error {
	if len(cfg.Env) > 0 || len(cfg.EnvPassthrough) > 0 {
		return fmt.Errorf(".container-sandbox.yaml declares env/envPassthrough, which reads the host environment; declare them on the sandbox backend in ~/.captain.yaml instead")
	}
	if len(cfg.Presets) > 0 {
		return fmt.Errorf(".container-sandbox.yaml declares presets, whose expansion mounts host caches and passes credentials through; declare presets on the sandbox backend in ~/.captain.yaml instead")
	}
	for _, volume := range cfg.Volumes {
		inside, err := pathWithin(volume.Source, cwd)
		if err != nil {
			return fmt.Errorf(".container-sandbox.yaml volume %q: %w", volume.Source, err)
		}
		if !inside {
			return fmt.Errorf(".container-sandbox.yaml mounts %q, outside the project directory; declare host mounts on the sandbox backend in ~/.captain.yaml instead", volume.Source)
		}
	}
	return nil
}

// pathWithin anchors relative paths on root and resolves every existing
// symlink component. A missing leaf is allowed because Docker creates bind
// sources, but its existing parent must still be canonicalized.
func pathWithin(path, root string) (bool, error) {
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
	if err != nil {
		return false, fmt.Errorf("resolve project directory: %w", err)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(resolvedRoot, path)
	}
	absolute, err := resolvePathAllowMissing(path)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(resolvedRoot, absolute)
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

// resolvePathAllowMissing resolves symlinks in the longest existing prefix and
// reattaches any missing suffix. This prevents a missing bind source below an
// outward-pointing symlink from passing a lexical containment check.
func resolvePathAllowMissing(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	current := absolute
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// loadTrustedOptions decodes the backend's own env/envPassthrough/volumes —
// user machine config, so host access is theirs to grant.
func (c *containerSandbox) loadTrustedOptions() error {
	c.optionEnv = map[string]string{}
	if raw, ok := c.options["env"].(map[string]any); ok {
		for key, value := range raw {
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("sandbox backend env %q must be a string", key)
			}
			c.optionEnv[key] = os.ExpandEnv(text)
		}
	}
	c.optionPassthrough = nil
	if raw, ok := c.options["envPassthrough"].([]any); ok {
		for _, item := range raw {
			if name, ok := item.(string); ok && name != "" {
				c.optionPassthrough = append(c.optionPassthrough, name)
			}
		}
	}
	c.optionVolumes = nil
	if raw, ok := c.options["volumes"].([]any); ok {
		for _, item := range raw {
			mount, ok := item.(string)
			if !ok || strings.Count(mount, ":") < 1 {
				return fmt.Errorf("sandbox backend volume %v must be a \"source:target[:ro]\" string", item)
			}
			c.optionVolumes = append(c.optionVolumes, mount)
		}
	}
	return nil
}

// Wrap builds the docker argv. Every environment value stays OUT of the argv:
// variables cross into the container by name only (-e KEY), and docker
// resolves each name from the client process environment, which Wrap returns
// as the authoritative env for the docker client.
func (c *containerSandbox) Wrap(_ context.Context, command string, args, declaredEnv []string) (string, []string, []string, error) {
	if c.cfg.Image == "" {
		return "", nil, nil, fmt.Errorf(
			"container sandbox has no image: run `captain container` in %s to generate .container-sandbox.yaml, or set image on the sandbox backend", c.cwd)
	}

	dockerArgs := []string{"run", "--rm", "-i", "-w", c.cwd, "-v", c.cwd + ":" + c.cwd}

	home := container.HostUser{Username: c.cfg.User.Username, UID: c.cfg.User.UID, GID: c.cfg.User.GID}.ContainerHome()
	presetEnv, presetVolumes := container.ResolveSandboxEnv(c.cfg.Presets, home)
	envArgs, clientEnv := c.dockerEnv(command, declaredEnv, presetEnv)
	dockerArgs = append(dockerArgs, envArgs...)
	for _, volume := range append(presetVolumes, container.ResolveDependencyVolumes(c.cfg.Presets, c.cwd, home)...) {
		dockerArgs = append(dockerArgs, "-v", volume.Source+":"+volume.Target)
	}
	// Repo-declared volumes were containment-checked in Prepare; trusted
	// backend-option volumes are the user's own grant.
	for _, volume := range c.cfg.Volumes {
		mount := volume.Source + ":" + volume.Target
		if volume.ReadOnly {
			mount += ":ro"
		}
		dockerArgs = append(dockerArgs, "-v", mount)
	}
	dockerArgs = append(dockerArgs, prefixEach("-v", c.optionVolumes)...)

	dockerArgs = append(dockerArgs, c.cfg.Image)
	dockerArgs = append(dockerArgs, command)
	dockerArgs = append(dockerArgs, args...)
	return "docker", dockerArgs, clientEnv, nil
}

// dockerEnv separates the Docker wire format from its client environment:
// argv receives names only, while resolved values replace entries in Env.
func (c *containerSandbox) dockerEnv(command string, declaredEnv, presetEnv []string) ([]string, []string) {
	clientEnv := os.Environ()
	var args []string
	passed := map[string]struct{}{}
	pass := func(name string) {
		if _, exists := passed[name]; exists {
			return
		}
		passed[name] = struct{}{}
		args = append(args, "-e", name)
	}
	for _, name := range append(cliCredentialEnv(command), c.optionPassthrough...) {
		if os.Getenv(name) != "" {
			pass(name)
		}
	}
	for _, item := range declaredEnv {
		if key, _, ok := strings.Cut(item, "="); ok && key != "" {
			pass(key)
			clientEnv = setEnvValue(clientEnv, item)
		}
	}
	for key, value := range c.optionEnv {
		pass(key)
		clientEnv = setEnvValue(clientEnv, key+"="+value)
	}
	for _, item := range presetEnv {
		if key, _, ok := strings.Cut(item, "="); ok && key != "" {
			pass(key)
			clientEnv = setEnvValue(clientEnv, item)
		}
	}
	return args, clientEnv
}

func setEnvValue(env []string, item string) []string {
	key, _, ok := strings.Cut(item, "=")
	if !ok || key == "" {
		return env
	}
	for i, existing := range env {
		if existingKey, _, ok := strings.Cut(existing, "="); ok && existingKey == key {
			env[i] = item
			return env
		}
	}
	return append(env, item)
}

func prefixEach(flag string, values []string) []string {
	var out []string
	for _, value := range values {
		out = append(out, flag, value)
	}
	return out
}

func (c *containerSandbox) Close() error { return nil }
