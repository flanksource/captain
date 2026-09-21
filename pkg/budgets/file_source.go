package budgets

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var budgetFileKey = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// FileSourceOptions identifies one YAML budget directory.
type FileSourceOptions struct {
	Dir      string
	Label    string
	Implicit bool
}

// NewFileSource creates a one-rule-per-YAML-file catalog source.
func NewFileSource(options FileSourceOptions) (Source, error) {
	dir := filepath.Clean(options.Dir)
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("budget dir %q must be absolute", options.Dir)
	}
	if !options.Implicit {
		info, err := os.Stat(dir)
		if err != nil {
			return nil, fmt.Errorf("budget dir %s: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("budget dir %s is not a directory", dir)
		}
	}
	label := strings.TrimSpace(options.Label)
	if label == "" {
		label = dir
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(dir)))[:16]
	return &fileSource{info: SourceInfo{Kind: SourceFile, ID: hash, Label: label, Root: dir, Writable: true, Implicit: options.Implicit}}, nil
}

type fileSource struct{ info SourceInfo }

func (s *fileSource) Info() SourceInfo { return s.info }
func (s *fileSource) Rules() Store     { return fileStore{s.info} }

type fileStore struct{ info SourceInfo }

func (s fileStore) List(ctx context.Context) ([]Rule, error) {
	entries, err := os.ReadDir(s.info.Root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && s.info.Implicit {
			return []Rule{}, nil
		}
		return nil, fmt.Errorf("list %s: %w", s.info.Label, err)
	}
	rules := []Rule{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		rule, err := s.Get(ctx, strings.TrimSuffix(entry.Name(), ".yaml"))
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	slices.SortFunc(rules, func(left, right Rule) int {
		return cmp.Or(cmp.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name)), cmp.Compare(left.Key, right.Key))
	})
	return rules, nil
}

func (s fileStore) Get(_ context.Context, key string) (Rule, error) {
	if !budgetFileKey.MatchString(key) {
		return Rule{}, fmt.Errorf("%w: invalid file key %q", ErrNotFound, key)
	}
	path := s.path(key)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Rule{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return Rule{}, fmt.Errorf("read %s: %w", path, err)
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return Rule{}, fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return Rule{}, fmt.Errorf("read %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var input RuleInput
	if err := decoder.Decode(&input); err != nil {
		return Rule{}, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}
	input, err = normalizeInput(input)
	if err != nil {
		return Rule{}, fmt.Errorf("%s: %w", path, err)
	}
	return buildRule(s.info, key, input, stat.ModTime()), nil
}

func (s fileStore) Create(ctx context.Context, input RuleInput) (Rule, error) {
	var err error
	input, err = normalizeInput(input)
	if err != nil {
		return Rule{}, err
	}
	key := slug(input.Name)
	if key == "" {
		return Rule{}, fmt.Errorf("%w: name %q yields no file name", ErrInvalid, input.Name)
	}
	if _, err := os.Stat(s.path(key)); err == nil {
		return Rule{}, fmt.Errorf("%w: %s already exists", ErrNameTaken, s.path(key))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Rule{}, err
	}
	if err := s.write(key, input); err != nil {
		return Rule{}, err
	}
	return s.Get(ctx, key)
}

func (s fileStore) Update(ctx context.Context, key string, input RuleInput) (Rule, error) {
	if !budgetFileKey.MatchString(key) {
		return Rule{}, fmt.Errorf("%w: invalid file key %q", ErrNotFound, key)
	}
	var err error
	input, err = normalizeInput(input)
	if err != nil {
		return Rule{}, err
	}
	if _, err := os.Stat(s.path(key)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Rule{}, fmt.Errorf("%w: %s", ErrNotFound, s.path(key))
		}
		return Rule{}, err
	}
	if err := s.write(key, input); err != nil {
		return Rule{}, err
	}
	return s.Get(ctx, key)
}

func (s fileStore) Delete(_ context.Context, key string) error {
	if !budgetFileKey.MatchString(key) {
		return fmt.Errorf("%w: invalid file key %q", ErrNotFound, key)
	}
	if err := os.Remove(s.path(key)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, s.path(key))
		}
		return err
	}
	return nil
}

func (s fileStore) write(key string, input RuleInput) error {
	if err := os.MkdirAll(s.info.Root, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode budget rule: %w", err)
	}
	temp, err := os.CreateTemp(s.info.Root, ".budget-*.yaml")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, s.path(key))
}

func (s fileStore) path(key string) string { return filepath.Join(s.info.Root, key+".yaml") }

func buildRule(source SourceInfo, key string, input RuleInput, updated time.Time) Rule {
	return Rule{ID: encodeID(source.ID, key), Key: key, Source: source, Name: input.Name, Match: input.Match,
		GroupBy: input.GroupBy, Amount: input.Amount, Window: input.Window, UpdatedAt: updated}
}

func slug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			builder.WriteRune(r)
		case builder.Len() > 0:
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}
