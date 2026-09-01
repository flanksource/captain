package cli

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
)

func buildPromptSources(ctx context.Context) ([]promptSource, error) {
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
		dir, err := resolvePromptDir(raw, base)
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

	cfg, exists, err := captainconfig.Load()
	if err != nil {
		return nil, err
	}
	if exists {
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
	for _, dir := range promptDirsFromContext(ctx) {
		if err := addLocal(dir, cwd); err != nil {
			return nil, err
		}
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
	return sources, nil
}

func promptDirsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	if dirs, ok := ctx.Value(promptDirsContextKey{}).([]string); ok {
		return dirs
	}
	return nil
}

func resolvePromptDir(raw, base string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", fmt.Errorf("prompt dir cannot be empty")
	}
	if strings.HasPrefix(dir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		switch {
		case dir == "~":
			dir = home
		case strings.HasPrefix(dir, "~/"):
			dir = filepath.Join(home, dir[2:])
		default:
			return "", fmt.Errorf("unsupported home-relative prompt dir %q", raw)
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("prompt dir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("prompt dir %s is not a directory", abs)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
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
	sources, err := buildPromptSources(ctx)
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
		if _, err := resolvePromptDir(dir, cwd); err != nil {
			return err
		}
	}
	return nil
}

var _ clicky.EntityItem = PromptSummary{}
var _ clickyapi.TableProvider = PromptSummary{}
