package container

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ShortcutsInput struct {
	Dir        string
	Config     SandboxConfig
	BuildArgs  BuildInput
	Components []Component
}

func GenerateShortcuts(input ShortcutsInput) error {
	cfg := input.Config
	if cfg.Name == "" {
		cfg.Name = DefaultContainerName(cfg.Image, os.Getenv("PWD"))
	}

	scripts := map[string]string{
		"sbx-build": buildScript(input.BuildArgs, input.Components),
		"sbx-start": startScript(cfg),
		"sbx-exec":  execScript(cfg.Name),
		"sbx-run":   runScript(),
		"sbx-stop":  stopScript(cfg.Name),
		"sbx-rm":    rmScript(cfg.Name),
	}

	for name, content := range scripts {
		path := filepath.Join(input.Dir, name)
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

func scriptHeader() string {
	return "#!/bin/bash\nset -e\nDIR=\"$(cd \"$(dirname \"$0\")/..\" && pwd)\"\n"
}

func buildScript(input BuildInput, components []Component) string {
	var b strings.Builder
	b.WriteString(scriptHeader())
	b.WriteString("CTX=\"$DIR/.container-sandbox\"\n\n")

	var copyLines, cleanupParts []string
	for _, c := range components {
		if !c.Selected || c.ContentKey != "" {
			continue
		}
		relPath := filepath.Join(string(c.Category), c.Name)
		dest := "\"$CTX/" + relPath + "\""
		src := shellQuote(c.SourcePath)
		if c.IsDir {
			copyLines = append(copyLines, fmt.Sprintf("cp -rf %s %s", src, dest))
			cleanupParts = append(cleanupParts, fmt.Sprintf("rm -rf %s", dest))
		} else {
			copyLines = append(copyLines, fmt.Sprintf("mkdir -p \"$(dirname %s)\"", dest))
			copyLines = append(copyLines, fmt.Sprintf("cp -f %s %s", src, dest))
			cleanupParts = append(cleanupParts, fmt.Sprintf("rm -f %s", dest))
		}
	}

	if len(copyLines) > 0 {
		b.WriteString("# Stage component files for build\n")
		for _, line := range copyLines {
			b.WriteString(line + "\n")
		}
		b.WriteString("\n# Cleanup staged files on exit\n")
		b.WriteString("trap '" + strings.Join(cleanupParts, "; ") + "' EXIT\n\n")
	}

	b.WriteString("docker build \\\n")
	fmt.Fprintf(&b, "  -t %s \\\n", input.Tag)
	fmt.Fprintf(&b, "  --build-arg USERNAME=%s \\\n", input.User.Username)
	fmt.Fprintf(&b, "  --build-arg USER_UID=%d \\\n", input.User.UID)
	fmt.Fprintf(&b, "  --build-arg USER_GID=%d \\\n", input.User.GID)
	b.WriteString("  -f \"$CTX/Dockerfile\" \\\n")
	b.WriteString("  \"$CTX\"\n")
	return b.String()
}

func startScript(cfg SandboxConfig) string {
	user := HostUser{Username: cfg.User.Username, UID: cfg.User.UID, GID: cfg.User.GID}
	containerHome := user.ContainerHome()

	var b strings.Builder
	b.WriteString(scriptHeader())
	fmt.Fprintf(&b, "NAME=%q\n", cfg.Name)
	b.WriteString("\n# Skip if already running\n")
	b.WriteString("if docker inspect --format '{{.State.Running}}' \"$NAME\" 2>/dev/null | grep -q true; then\n")
	b.WriteString("  echo \"Container $NAME is already running\"\n")
	b.WriteString("  exit 0\n")
	b.WriteString("fi\n\n")
	b.WriteString("# Remove stopped container with same name\n")
	b.WriteString("docker rm -f \"$NAME\" 2>/dev/null || true\n\n")

	b.WriteString("docker run -d \\\n")
	b.WriteString("  -w \"$PWD\" \\\n")
	b.WriteString("  -v \"$PWD:$PWD\" \\\n")
	b.WriteString("  --name \"$NAME\" \\\n")

	b.WriteString("  ${CLAUDE_CODE_OAUTH_TOKEN:+-e CLAUDE_CODE_OAUTH_TOKEN=\"$CLAUDE_CODE_OAUTH_TOKEN\"} \\\n")
	b.WriteString("  ${ANTHROPIC_API_KEY:+-e ANTHROPIC_API_KEY=\"$ANTHROPIC_API_KEY\"} \\\n")

	sandboxEnvVars, sandboxVolumes := ResolveSandboxEnv(cfg.Presets, containerHome)
	for _, e := range sandboxEnvVars {
		fmt.Fprintf(&b, "  -e %s \\\n", shellQuote(e))
	}

	for _, v := range sandboxVolumes {
		fmt.Fprintf(&b, "  -v %s:%s \\\n", v.Source, v.Target)
	}

	pwd := os.Getenv("PWD")
	for _, v := range ResolveDependencyVolumes(cfg.Presets, pwd, containerHome) {
		fmt.Fprintf(&b, "  -v %s:%s \\\n", v.Source, v.Target)
	}

	for _, name := range cfg.EnvPassthrough {
		fmt.Fprintf(&b, "  ${%s:+-e %s=\"$%s\"} \\\n", name, name, name)
	}

	for k, v := range cfg.Env {
		fmt.Fprintf(&b, "  -e %s \\\n", shellQuote(k+"="+os.ExpandEnv(v)))
	}

	for _, v := range cfg.Volumes {
		mount := v.Source + ":" + v.Target
		if v.ReadOnly {
			mount += ":ro"
		}
		fmt.Fprintf(&b, "  -v %s \\\n", mount)
	}

	fmt.Fprintf(&b, "  %s \\\n", cfg.Image)
	b.WriteString("  sleep infinity\n\n")
	b.WriteString("echo \"Container $NAME started\"\n")
	return b.String()
}

func execScript(name string) string {
	var b strings.Builder
	b.WriteString(scriptHeader())
	fmt.Fprintf(&b, "NAME=%q\n", name)
	b.WriteString("if [ $# -eq 0 ]; then\n")
	b.WriteString("  docker exec -it \"$NAME\" bash\n")
	b.WriteString("else\n")
	b.WriteString("  docker exec -it \"$NAME\" \"$@\"\n")
	b.WriteString("fi\n")
	return b.String()
}

func runScript() string {
	var b strings.Builder
	b.WriteString(scriptHeader())
	b.WriteString("\"$DIR/.container-sandbox/sbx-start\"\n")
	b.WriteString("\"$DIR/.container-sandbox/sbx-exec\" \"$@\"\n")
	return b.String()
}

func stopScript(name string) string {
	var b strings.Builder
	b.WriteString(scriptHeader())
	fmt.Fprintf(&b, "docker stop %q\n", name)
	return b.String()
}

func rmScript(name string) string {
	var b strings.Builder
	b.WriteString(scriptHeader())
	fmt.Fprintf(&b, "docker rm -f %q\n", name)
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
