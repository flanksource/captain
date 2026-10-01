package budgets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// CatalogOptions supplies lazy read and write database openers. Write may be
// nil for a read-only catalog, such as the one chat admission evaluates.
type CatalogOptions struct {
	Read  func(context.Context) (*database.DB, error)
	Write func(context.Context) (*database.DB, error)
}

// Catalog reads and writes budget rules. The database is the only source.
type Catalog struct {
	options CatalogOptions
}

// NewCatalog creates a database-backed budget rule catalog.
func NewCatalog(options CatalogOptions) (*Catalog, error) {
	if options.Read == nil {
		return nil, fmt.Errorf("budget catalog requires a Read opener")
	}
	return &Catalog{options: options}, nil
}

// List returns the live rules ordered by name.
func (c *Catalog) List(ctx context.Context) ([]Rule, error) {
	db, err := c.options.Read(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.ListBudgetRules(ctx)
	if err != nil {
		return nil, mapDBError(err)
	}
	out := make([]Rule, 0, len(rows))
	for _, row := range rows {
		out = append(out, ruleFrom(row))
	}
	return out, nil
}

// Get resolves a live rule by id or, case-insensitively, by name.
func (c *Catalog) Get(ctx context.Context, ref string) (Rule, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		db, err := c.options.Read(ctx)
		if err != nil {
			return Rule{}, err
		}
		row, err := db.GetBudgetRule(ctx, id)
		if err != nil {
			return Rule{}, mapDBError(err)
		}
		return ruleFrom(*row), nil
	}
	rules, err := c.List(ctx)
	if err != nil {
		return Rule{}, err
	}
	for _, rule := range rules {
		if strings.EqualFold(rule.Name, ref) {
			return rule, nil
		}
	}
	return Rule{}, fmt.Errorf("%w: %q", ErrNotFound, ref)
}

func (c *Catalog) Create(ctx context.Context, input RuleInput) (Rule, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return Rule{}, err
	}
	db, err := c.writer(ctx)
	if err != nil {
		return Rule{}, err
	}
	row, err := db.CreateBudgetRule(ctx, dbInput(input))
	if err != nil {
		return Rule{}, mapDBError(err)
	}
	return ruleFrom(*row), nil
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
	db, err := c.writer(ctx)
	if err != nil {
		return Rule{}, err
	}
	row, err := db.UpdateBudgetRule(ctx, current.ID, dbInput(input))
	if err != nil {
		return Rule{}, mapDBError(err)
	}
	return ruleFrom(*row), nil
}

// Delete soft-deletes a rule; spend it already attributed stays attached.
func (c *Catalog) Delete(ctx context.Context, ref string) error {
	current, err := c.Get(ctx, ref)
	if err != nil {
		return err
	}
	db, err := c.writer(ctx)
	if err != nil {
		return err
	}
	return mapDBError(db.DeleteBudgetRule(ctx, current.ID))
}

func (c *Catalog) writer(ctx context.Context) (*database.DB, error) {
	if c.options.Write == nil {
		return nil, ErrReadOnly
	}
	return c.options.Write(ctx)
}

func ruleFrom(row database.BudgetRule) Rule {
	return Rule{ID: row.ID, Name: row.Name,
		Match:   RuleMatch{Dimensions: row.Match.Dimensions, Models: row.Match.Models},
		GroupBy: row.GroupBy, Amount: row.Amount, Window: row.Window,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func dbInput(input RuleInput) database.BudgetRuleInput {
	return database.BudgetRuleInput{Name: input.Name,
		Match:   database.BudgetRuleMatch{Dimensions: input.Match.Dimensions, Models: input.Match.Models},
		GroupBy: input.GroupBy, Amount: input.Amount, Window: input.Window}
}

func mapDBError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, database.ErrBudgetRuleNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errors.Is(err, database.ErrBudgetNameTaken):
		return fmt.Errorf("%w: %w", ErrNameTaken, err)
	case errors.Is(err, database.ErrBudgetInvalid):
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	default:
		return err
	}
}
