package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
)

type promptSourceOptions struct {
	Config *captainconfig.Config
}

func buildPromptSources(ctx context.Context, options promptSourceOptions) ([]promptSource, error) {
	sources := []promptSource{{
		Kind:     "embedded",
		ID:       "embedded",
		Label:    "Embedded examples",
		WalkRoot: "testdata",
		FS:       promptlib.Examples,
		Writable: false,
	}}

	seen := map[string]bool{}
	addLocal := func(raw, base string) error {
		dir, err := promptlib.ResolveDir(raw, base)
		if err != nil {
			return err
		}
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		sources = append(sources, promptSource{
			Kind:     "local",
			ID:       hashPromptDir(dir),
			Label:    dir,
			Root:     dir,
			Writable: true,
		})
		return nil
	}

	cfg := options.Config
	if cfg == nil {
		loaded, err := loadSavedConfig()
		if err != nil {
			return nil, err
		}
		cfg = &loaded
	}
	if len(cfg.Prompts.Dirs) > 0 {
		configPath, err := captainconfig.Path()
		if err != nil {
			return nil, err
		}
		base := filepath.Dir(configPath)
		for _, dir := range cfg.Prompts.Dirs {
			if err := addLocal(dir, base); err != nil {
				return nil, err
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	scoped := promptSourcesFromContext(ctx)
	for _, dir := range scoped.Dirs {
		if err := addLocal(dir, cwd); err != nil {
			return nil, err
		}
	}
	for _, host := range scoped.FS {
		source, err := hostPromptSource(host, sources)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	if _, ok := firstWritableSource(sources); !ok {
		dir := filepath.Join(cwd, ".captain", "prompts")
		sources = append(sources, promptSource{
			Kind:     "local",
			ID:       hashPromptDir(dir),
			Label:    dir,
			Root:     dir,
			Writable: true,
			Implicit: true,
		})
	}
	// The user prompt directory comes after the fallback so it never becomes
	// the default save target in place of the repository's .captain/prompts.
	configHome, err := captainconfig.Home()
	if err != nil {
		return nil, err
	}
	if dir := filepath.Join(configHome, "prompts"); !seen[dir] {
		sources = append(sources, promptSource{
			Kind:     "local",
			ID:       hashPromptDir(dir),
			Label:    dir,
			Root:     dir,
			Writable: true,
			Implicit: true,
		})
	}
	return sources, nil
}

func promptSourcesFromContext(ctx context.Context) PromptSources {
	if ctx == nil {
		return PromptSources{}
	}
	sources, _ := ctx.Value(promptSourcesContextKey{}).(PromptSources)
	return sources
}

func promptLookupSources(sources []promptSource) []promptSource {
	ordered := slices.Clone(sources)
	slices.SortStableFunc(ordered, func(a, b promptSource) int {
		if (a.FS == nil) == (b.FS == nil) {
			return 0
		}
		if a.FS == nil {
			return -1
		}
		return 1
	})
	return ordered
}

// hostPromptSource turns a host's fs.FS into a read-only source whose id is its
// label's slug. A label that cannot be addressed, or whose id another source
// already holds, is refused: two sources under one id make every prompt id they
// encode ambiguous.
func hostPromptSource(host PromptFS, existing []promptSource) (promptSource, error) {
	label := strings.TrimSpace(host.Label)
	if label == "" {
		return promptSource{}, fmt.Errorf("prompt source label is required")
	}
	if host.FS == nil {
		return promptSource{}, fmt.Errorf("prompt source %q has no fs.FS", label)
	}
	id := slugPromptName(label)
	if id == "" {
		return promptSource{}, fmt.Errorf("prompt source label %q has no usable id", label)
	}
	for _, source := range existing {
		if source.ID == id {
			return promptSource{}, fmt.Errorf("prompt source %q: id %q is already registered by %q", label, id, source.Label)
		}
	}
	return promptSource{Kind: "embedded", ID: id, Label: label, WalkRoot: ".", FS: host.FS}, nil
}

func writableSourceByID(sources []promptSource, id string) (promptSource, bool) {
	for _, source := range sources {
		if source.ID == id && source.Writable {
			return source, true
		}
	}
	return promptSource{}, false
}

func firstWritableSource(sources []promptSource) (promptSource, bool) {
	for _, source := range sources {
		if source.Writable {
			return source, true
		}
	}
	return promptSource{}, false
}

func safeLocalPromptPath(source promptSource, rel string) (string, error) {
	cleanRel, err := localPromptRel(rel)
	if err != nil {
		return "", err
	}
	return filepath.Join(source.Root, cleanRel), nil
}

func localPromptRel(rel string) (string, error) {
	cleanRel := strings.TrimPrefix(filepath.Clean(filepath.FromSlash(rel)), string(filepath.Separator))
	if cleanRel == "." || cleanRel == "" || filepath.IsAbs(rel) || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || cleanRel == ".." {
		return "", fmt.Errorf("invalid prompt path %q", rel)
	}
	if filepath.Ext(cleanRel) != ".prompt" {
		return "", fmt.Errorf("prompt path must end with .prompt")
	}
	return cleanRel, nil
}

func normalizeWriteRelPath(relPath, name string) (string, error) {
	rel := strings.TrimSpace(relPath)
	if rel == "" {
		rel = slugPromptName(name)
	}
	if rel == "" {
		return "", fmt.Errorf("prompt name or path required")
	}
	if !strings.HasSuffix(rel, ".prompt") {
		rel += ".prompt"
	}
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if strings.HasPrefix(rel, "../") || rel == ".." || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("invalid prompt path %q", relPath)
	}
	return rel, nil
}

func slugPromptName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func encodePromptID(kind, sourceID, rel string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + "\x00" + sourceID + "\x00" + filepath.ToSlash(rel)))
}

func decodePromptID(id string) (promptRef, error) {
	data, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return promptRef{}, fmt.Errorf("invalid prompt id: %w", err)
	}
	parts := strings.SplitN(string(data), "\x00", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return promptRef{}, fmt.Errorf("invalid prompt id")
	}
	return promptRef{Kind: parts[0], SourceID: parts[1], RelPath: filepath.ToSlash(parts[2])}, nil
}

// PromptSourceInfo is one discovered prompt source as the editor's destination
// picker sees it, so a configured-but-empty writable directory (or the implicit
// .captain/prompts fallback) is still offered as a save target.
type PromptSourceInfo struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Root     string `json:"root,omitempty"`
	Writable bool   `json:"writable"`
	Implicit bool   `json:"implicit,omitempty"`
}

func promptSourceInfos(ctx context.Context) ([]PromptSourceInfo, error) {
	sources, err := buildPromptSources(ctx, promptSourceOptions{})
	if err != nil {
		return nil, err
	}
	infos := make([]PromptSourceInfo, 0, len(sources))
	for _, source := range sources {
		infos = append(infos, PromptSourceInfo{
			ID: source.ID, Kind: source.Kind, Label: source.Label, Root: source.Root,
			Writable: source.Writable, Implicit: source.Implicit,
		})
	}
	return infos, nil
}

func hashPromptDir(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:])[:12]
}

func ValidatePromptDirs(dirs []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if _, err := promptlib.ResolveDir(dir, cwd); err != nil {
			return err
		}
	}
	return nil
}

var _ clicky.EntityItem = PromptSummary{}
var _ clickyapi.TableProvider = PromptSummary{}
