package runtimeprofiles

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const fileExt = ".yaml"

// keyPattern is the file name stem a record may have. It is deliberately
// narrower than what a file system allows, so a key never needs escaping in an
// id, a URL, or a shell.
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// FileSourceOptions describes one directory of YAML records of a single kind.
type FileSourceOptions struct {
	Kind Kind
	// Dir is the absolute directory scanned non-recursively for *.yaml files.
	Dir   string
	Label string
	// Implicit marks a well-known location (~/.config/captain, .captain) that
	// may be absent: it lists as empty and is created on the first write. A
	// configured directory must already exist.
	Implicit bool
}

// NewFileSource opens a directory as a catalog source.
func NewFileSource(options FileSourceOptions) (Source, error) {
	if options.Kind != KindPreset && options.Kind != KindProfile {
		return nil, fmt.Errorf("runtime file source %s: unknown record kind %q", options.Dir, options.Kind)
	}
	dir := filepath.Clean(options.Dir)
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("runtime %s dir %q must be absolute", options.Kind, options.Dir)
	}
	if !options.Implicit {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("runtime %s dir %s: %w", options.Kind, dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("runtime %s dir %s is not a directory", options.Kind, dir)
		}
	}
	label := strings.TrimSpace(options.Label)
	if label == "" {
		label = dir
	}
	return &fileSource{info: SourceInfo{
		Kind: SourceFile, ID: hashDir(dir), Label: label, Root: dir,
		Writable: true, Implicit: options.Implicit, Records: []Kind{options.Kind},
	}}, nil
}

type fileSource struct{ info SourceInfo }

func (s *fileSource) Info() SourceInfo { return s.info }

func (s *fileSource) Presets() Store[Preset, PresetInput] {
	if !s.info.Holds(KindPreset) {
		return nil
	}
	return fileStore[Preset, PresetInput]{info: s.info, kind: KindPreset}
}

func (s *fileSource) Profiles() Store[Profile, ProfileInput] {
	if !s.info.Holds(KindProfile) {
		return nil
	}
	return fileStore[Profile, ProfileInput]{info: s.info, kind: KindProfile}
}

type fileStore[R record, I input[R, I]] struct {
	info SourceInfo
	kind Kind
}

func (s fileStore[R, I]) List(ctx context.Context) ([]R, error) {
	entries, err := os.ReadDir(s.info.Root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && s.info.Implicit {
			return []R{}, nil
		}
		return nil, fmt.Errorf("list %s: %w", s.info.Label, err)
	}
	records := []R{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || filepath.Ext(name) != fileExt {
			continue
		}
		key := strings.TrimSuffix(name, fileExt)
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("%w: %s: file name %q is not a valid %s key (%s)",
				ErrInvalid, s.info.Label, name, s.kind, keyPattern)
		}
		record, err := s.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(left, right R) int {
		leftMeta, rightMeta := left.meta(), right.meta()
		return cmp.Or(
			cmp.Compare(strings.ToLower(leftMeta.Name), strings.ToLower(rightMeta.Name)),
			cmp.Compare(leftMeta.Key, rightMeta.Key),
		)
	})
	return records, nil
}

func (s fileStore[R, I]) Get(_ context.Context, key string) (R, error) {
	var zero R
	if !keyPattern.MatchString(key) {
		return zero, s.notFound(key)
	}
	path := s.path(key)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return zero, s.notFound(key)
		}
		return zero, fmt.Errorf("read %s: %w", path, err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return zero, fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return zero, fmt.Errorf("read %s: %w", path, err)
	}
	in, err := decodeFile[I](path, data)
	if err != nil {
		return zero, err
	}
	in = in.trimmed()
	if err := in.validate(); err != nil {
		return zero, fmt.Errorf("%s: %w", path, err)
	}
	return in.build(recordMeta{
		ID: EncodeID(s.kind, s.info.ID, key), Key: key, Source: s.info, UpdatedAt: stat.ModTime(),
	}), nil
}

// Create derives the key from the name. Two names that slug to the same key
// collide even when they differ as names, so the file's existence is checked
// rather than only the catalog-wide name.
func (s fileStore[R, I]) Create(ctx context.Context, in I) (R, error) {
	var zero R
	in = in.trimmed()
	if err := in.validate(); err != nil {
		return zero, err
	}
	key := slug(in.name())
	if key == "" {
		return zero, fmt.Errorf("%w: %s name %q yields no file name", ErrInvalid, s.kind, in.name())
	}
	exists, err := s.exists(key)
	if err != nil {
		return zero, err
	}
	if exists {
		return zero, fmt.Errorf("%w: %s already holds %s%s", ErrNameTaken, s.info.Label, key, fileExt)
	}
	if err := s.write(key, in); err != nil {
		return zero, err
	}
	return s.Get(ctx, key)
}

// Update rewrites the file in place; the key, and so the id, never changes on
// rename.
func (s fileStore[R, I]) Update(ctx context.Context, key string, in I) (R, error) {
	var zero R
	exists, err := s.exists(key)
	if err != nil {
		return zero, err
	}
	if !exists {
		return zero, s.notFound(key)
	}
	in = in.trimmed()
	if err := in.validate(); err != nil {
		return zero, err
	}
	if err := s.write(key, in); err != nil {
		return zero, err
	}
	return s.Get(ctx, key)
}

func (s fileStore[R, I]) Delete(_ context.Context, key string) error {
	if !keyPattern.MatchString(key) {
		return s.notFound(key)
	}
	if err := os.Remove(s.path(key)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s.notFound(key)
		}
		return fmt.Errorf("delete %s: %w", s.path(key), err)
	}
	return nil
}

func (s fileStore[R, I]) exists(key string) (bool, error) {
	if !keyPattern.MatchString(key) {
		return false, nil
	}
	_, err := os.Stat(s.path(key))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", s.path(key), err)
}

func (s fileStore[R, I]) notFound(key string) error {
	return fmt.Errorf("%w: %s %q in %s", ErrNotFound, s.kind, key, s.info.Label)
}

func (s fileStore[R, I]) path(key string) string { return filepath.Join(s.info.Root, key+fileExt) }

func (s fileStore[R, I]) write(key string, in I) error {
	data, err := encodeFile(in)
	if err != nil {
		return fmt.Errorf("encode %s %q: %w", s.kind, key, err)
	}
	if err := os.MkdirAll(s.info.Root, 0o755); err != nil {
		return fmt.Errorf("ensure %s: %w", s.info.Root, err)
	}
	root, err := os.OpenRoot(s.info.Root)
	if err != nil {
		return fmt.Errorf("open %s: %w", s.info.Root, err)
	}
	defer root.Close()
	return writeFileAtomic(root, key+fileExt, data)
}
