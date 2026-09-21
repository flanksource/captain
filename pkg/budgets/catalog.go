package budgets

import (
	"context"
	"fmt"
	"strings"
)

// SourceKind distinguishes database and YAML-directory sources.
type SourceKind string

const (
	SourceDB   SourceKind = "db"
	SourceFile SourceKind = "file"
)

// SourceInfo describes one budget catalog source.
type SourceInfo struct {
	Kind     SourceKind `json:"kind"`
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	Root     string     `json:"root,omitempty"`
	Writable bool       `json:"writable"`
	Implicit bool       `json:"implicit,omitempty"`
}

// Store is the persistence contract implemented by each catalog source.
type Store interface {
	List(context.Context) ([]Rule, error)
	Get(context.Context, string) (Rule, error)
	Create(context.Context, RuleInput) (Rule, error)
	Update(context.Context, string, RuleInput) (Rule, error)
	Delete(context.Context, string) error
}

// Source exposes one budget rule store and its catalog metadata.
type Source interface {
	Info() SourceInfo
	Rules() Store
}

// Catalog combines budget rule sources and routes writes to their owner.
type Catalog struct {
	sources []Source
	byID    map[string]Source
}

// NewCatalog registers sources in read order. Source IDs must be unique.
func NewCatalog(sources ...Source) (*Catalog, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("budget catalog requires at least one source")
	}
	catalog := &Catalog{byID: make(map[string]Source, len(sources))}
	for _, source := range sources {
		info := source.Info()
		if info.ID == "" || catalog.byID[info.ID] != nil {
			return nil, fmt.Errorf("budget source id %q is empty or registered twice", info.ID)
		}
		catalog.sources = append(catalog.sources, source)
		catalog.byID[info.ID] = source
	}
	return catalog, nil
}

// Sources lists the configured catalog sources.
func (c *Catalog) Sources() []SourceInfo {
	out := make([]SourceInfo, 0, len(c.sources))
	for _, source := range c.sources {
		out = append(out, source.Info())
	}
	return out
}

func (c *Catalog) List(ctx context.Context) ([]Rule, error) {
	rules := []Rule{}
	for _, source := range c.sources {
		items, err := source.Rules().List(ctx)
		if err != nil {
			return nil, err
		}
		for index := range items {
			normalized, err := normalizeInput(RuleInput{
				Name: items[index].Name, Match: items[index].Match, GroupBy: items[index].GroupBy,
				Amount: items[index].Amount, Window: items[index].Window,
			})
			if err != nil {
				return nil, fmt.Errorf("budget source %s rule %q: %w", source.Info().Label, items[index].Name, err)
			}
			items[index].Name, items[index].Match, items[index].GroupBy = normalized.Name, normalized.Match, normalized.GroupBy
			items[index].Amount, items[index].Window = normalized.Amount, normalized.Window
		}
		rules = append(rules, items...)
	}
	return rules, nil
}

func (c *Catalog) Get(ctx context.Context, ref string) (Rule, error) {
	if sourceID, key, ok := decodeID(ref); ok {
		source := c.byID[sourceID]
		if source == nil {
			return Rule{}, fmt.Errorf("%w: source %q", ErrNotFound, sourceID)
		}
		return source.Rules().Get(ctx, key)
	}
	var matches []Rule
	rules, err := c.List(ctx)
	if err != nil {
		return Rule{}, err
	}
	for _, rule := range rules {
		if strings.EqualFold(rule.Name, strings.TrimSpace(ref)) {
			matches = append(matches, rule)
		}
	}
	if len(matches) == 0 {
		return Rule{}, fmt.Errorf("%w: %q", ErrNotFound, ref)
	}
	if len(matches) > 1 {
		return Rule{}, fmt.Errorf("%w: %q; use an id", ErrAmbiguous, ref)
	}
	return matches[0], nil
}

func (c *Catalog) Create(ctx context.Context, target string, input RuleInput) (Rule, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return Rule{}, err
	}
	if err := c.requireNameFree(ctx, input.Name, ""); err != nil {
		return Rule{}, err
	}
	source, err := c.target(target)
	if err != nil {
		return Rule{}, err
	}
	if !source.Info().Writable {
		return Rule{}, fmt.Errorf("%w: %s", ErrReadOnly, source.Info().Label)
	}
	return source.Rules().Create(ctx, input)
}

func (c *Catalog) Update(ctx context.Context, ref string, input RuleInput) (Rule, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return Rule{}, err
	}
	current, err := c.Get(ctx, ref)
	if err != nil {
		return Rule{}, err
	}
	if !current.Source.Writable {
		return Rule{}, fmt.Errorf("%w: %s", ErrReadOnly, current.Source.Label)
	}
	if err := c.requireNameFree(ctx, input.Name, current.ID); err != nil {
		return Rule{}, err
	}
	return c.byID[current.Source.ID].Rules().Update(ctx, current.Key, input)
}

func (c *Catalog) Delete(ctx context.Context, ref string) error {
	current, err := c.Get(ctx, ref)
	if err != nil {
		return err
	}
	if !current.Source.Writable {
		return fmt.Errorf("%w: %s", ErrReadOnly, current.Source.Label)
	}
	return c.byID[current.Source.ID].Rules().Delete(ctx, current.Key)
}

func (c *Catalog) target(id string) (Source, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		for _, source := range c.sources {
			if source.Info().Kind == SourceDB {
				return source, nil
			}
		}
		return nil, fmt.Errorf("budget catalog has no database source; name a target source")
	}
	if source := c.byID[id]; source != nil {
		return source, nil
	}
	return nil, fmt.Errorf("unknown budget source %q", id)
}

func (c *Catalog) requireNameFree(ctx context.Context, name, except string) error {
	rules, err := c.List(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.ID != except && strings.EqualFold(rule.Name, name) {
			return fmt.Errorf("%w: %q already exists in %s", ErrNameTaken, rule.Name, rule.Source.Label)
		}
	}
	return nil
}

func encodeID(source, key string) string { return "budget:" + source + ":" + key }

func decodeID(id string) (string, string, bool) {
	parts := strings.SplitN(strings.TrimSpace(id), ":", 3)
	return value(parts, 1), value(parts, 2), len(parts) == 3 && parts[0] == "budget" && parts[1] != "" && parts[2] != ""
}

func value(parts []string, index int) string {
	if index >= len(parts) {
		return ""
	}
	return parts[index]
}
