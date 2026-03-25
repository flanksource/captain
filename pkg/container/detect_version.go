package container

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var goVersionRe = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)`)
var semverMajorRe = regexp.MustCompile(`(\d+)`)

func DetectGoVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	if m := goVersionRe.FindSubmatch(data); len(m) >= 2 {
		return string(m[1])
	}
	return ""
}

func DetectPythonVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".python-version"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	if line == "" {
		return ""
	}
	return line
}

func DetectRustVersion(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "rust-toolchain.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "channel") {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				return strings.Trim(strings.TrimSpace(parts[1]), "\"'")
			}
		}
	}
	return ""
}

func DetectNodeMajorVersion(dir string) string {
	if v := nodeVersionFromPackageJSON(dir); v != "" {
		return v
	}
	return nodeVersionFromHost()
}

func nodeVersionFromPackageJSON(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Engines.Node == "" {
		return ""
	}
	if m := semverMajorRe.FindString(pkg.Engines.Node); m != "" {
		return m
	}
	return ""
}

func nodeVersionFromHost() string {
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	v = strings.TrimPrefix(v, "v")
	if m := semverMajorRe.FindString(v); m != "" {
		return m
	}
	return ""
}

func DetectVersions(dir string) map[string]string {
	versions := make(map[string]string)
	if v := DetectGoVersion(dir); v != "" {
		versions["golang"] = v
	}
	if v := DetectPythonVersion(dir); v != "" {
		versions["python"] = v
	}
	if v := DetectRustVersion(dir); v != "" {
		versions["rust"] = v
	}
	if v := DetectNodeMajorVersion(dir); v != "" {
		versions["node"] = v
	}
	return versions
}
