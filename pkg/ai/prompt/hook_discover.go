package prompt

import (
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
)

// HookSource is one place hook prompts are discovered from; Label names it in
// errors and provenance.
type HookSource struct {
	FS    fs.FS
	Label string
}

// HookFile is one discovered hook prompt for an event, before the event owner
// validates and compiles it.
type HookFile struct {
	// Name is the prompt file's base name without .prompt.
	Name string
	// Source is where the hook was loaded from: "<label>:<file>".
	Source   string
	Template *Template
	Spec     HookSpec
}

// DiscoverHookFiles walks sources in order for event's hooks. A same-named
// prompt in a later source replaces an earlier one, `hook: {disabled: true}`
// removes it, and hooks of other events are ignored. Any prompt that cannot be
// read or parsed fails discovery naming its source. Files are in name order.
func DiscoverHookFiles(event string, sources ...HookSource) ([]HookFile, error) {
	byName := map[string]HookFile{}
	for _, src := range sources {
		if src.FS == nil {
			return nil, fmt.Errorf("hook source %q has no FS", src.Label)
		}
		if err := discoverHookFiles(event, src, byName); err != nil {
			return nil, err
		}
	}
	return slices.SortedFunc(maps.Values(byName), func(a, b HookFile) int { return strings.Compare(a.Name, b.Name) }), nil
}

func discoverHookFiles(event string, src HookSource, byName map[string]HookFile) error {
	return fs.WalkDir(src.FS, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Ext(file) != ".prompt" {
			return nil
		}
		tmpl, err := LoadFS(src.FS, file)
		if err != nil {
			return err
		}
		source := src.Label + ":" + file
		spec, err := tmpl.Hook()
		if err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		if spec == nil {
			return nil
		}
		name := strings.TrimSuffix(path.Base(file), ".prompt")
		if !HookNameRe.MatchString(name) {
			return fmt.Errorf("%s: hook name %q must match %s", source, name, HookNameRe)
		}
		if spec.Disabled {
			delete(byName, name)
			return nil
		}
		if spec.On == event {
			byName[name] = HookFile{Name: name, Source: source, Template: tmpl, Spec: *spec}
		}
		return nil
	})
}
