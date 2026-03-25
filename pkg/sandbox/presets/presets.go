package presets

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var presetsFS embed.FS

type CaptainPreset struct {
	Install        []string `yaml:"install,omitempty"`
	DependencyDirs []string `yaml:"dependencyDirs,omitempty"`
	BaseImage      string   `yaml:"baseImage,omitempty"`
}

func InstallSnippets(names []string) []string {
	var result []string
	for _, name := range names {
		data, err := Get(name)
		if err != nil {
			continue
		}
		var p CaptainPreset
		if yaml.Unmarshal(data, &p) != nil {
			continue
		}
		result = append(result, p.Install...)
	}
	return result
}

func GetDependencyDirs(names []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, name := range names {
		data, err := Get(name)
		if err != nil {
			continue
		}
		var p CaptainPreset
		if yaml.Unmarshal(data, &p) != nil {
			continue
		}
		for _, d := range p.DependencyDirs {
			if !seen[d] {
				seen[d] = true
				result = append(result, d)
			}
		}
	}
	return result
}

func GetBaseImage(names []string, versions map[string]string) string {
	for _, name := range names {
		data, err := Get(name)
		if err != nil {
			continue
		}
		var p CaptainPreset
		if yaml.Unmarshal(data, &p) != nil || p.BaseImage == "" {
			continue
		}
		return ResolveTemplate(p.BaseImage, versions)
	}
	return ""
}

func ResolveInstallSnippets(names []string, versions map[string]string) []string {
	var result []string
	for _, name := range names {
		data, err := Get(name)
		if err != nil {
			continue
		}
		var p CaptainPreset
		if yaml.Unmarshal(data, &p) != nil {
			continue
		}
		for _, s := range p.Install {
			result = append(result, ResolveTemplate(s, versions))
		}
	}
	return result
}

func ResolveTemplate(tmpl string, versions map[string]string) string {
	result := tmpl
	for k, v := range versions {
		result = strings.ReplaceAll(result, "{{."+k+"Version}}", v)
		result = strings.ReplaceAll(result, "{{."+k+"}}", v)
	}
	return result
}

func Get(name string) ([]byte, error) {
	data, err := presetsFS.ReadFile(name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("unknown preset %q", name)
	}
	return data, nil
}

func List() []string {
	entries, err := presetsFS.ReadDir(".")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(names)
	return names
}
