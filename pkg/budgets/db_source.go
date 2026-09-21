package budgets

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

const DBSourceID = "db"

// DBSourceOptions supplies lazy read and write database openers.
type DBSourceOptions struct {
	Read  func(context.Context) (*database.DB, error)
	Write func(context.Context) (*database.DB, error)
}

// NewDBSource creates the database-backed catalog source.
func NewDBSource(options DBSourceOptions) (Source, error) {
	if options.Read == nil {
		return nil, fmt.Errorf("budget database source requires a Read opener")
	}
	return &dbSource{options: options, info: SourceInfo{Kind: SourceDB, ID: DBSourceID, Label: "Database", Writable: options.Write != nil}}, nil
}

type dbSource struct {
	options DBSourceOptions
	info    SourceInfo
}

func (s *dbSource) Info() SourceInfo { return s.info }
func (s *dbSource) Rules() Store     { return dbStore{s} }

type dbStore struct{ *dbSource }

func (s dbStore) List(ctx context.Context) ([]Rule, error) {
	db, err := s.options.Read(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.ListBudgetRules(ctx)
	if err != nil {
		return nil, mapDBError(err)
	}
	out := make([]Rule, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.rule(row))
	}
	return out, nil
}

func (s dbStore) Get(ctx context.Context, key string) (Rule, error) {
	id, err := uuid.Parse(key)
	if err != nil {
		return Rule{}, fmt.Errorf("%w: invalid database key %q", ErrNotFound, key)
	}
	db, err := s.options.Read(ctx)
	if err != nil {
		return Rule{}, err
	}
	row, err := db.GetBudgetRule(ctx, id)
	if err != nil {
		return Rule{}, mapDBError(err)
	}
	return s.rule(*row), nil
}

func (s dbStore) Create(ctx context.Context, input RuleInput) (Rule, error) {
	var err error
	input, err = normalizeInput(input)
	if err != nil {
		return Rule{}, err
	}
	db, err := s.writer(ctx)
	if err != nil {
		return Rule{}, err
	}
	row, err := db.CreateBudgetRule(ctx, dbInput(input))
	if err != nil {
		return Rule{}, mapDBError(err)
	}
	return s.rule(*row), nil
}

func (s dbStore) Update(ctx context.Context, key string, input RuleInput) (Rule, error) {
	id, err := uuid.Parse(key)
	if err != nil {
		return Rule{}, fmt.Errorf("%w: invalid database key %q", ErrNotFound, key)
	}
	input, err = normalizeInput(input)
	if err != nil {
		return Rule{}, err
	}
	db, err := s.writer(ctx)
	if err != nil {
		return Rule{}, err
	}
	row, err := db.UpdateBudgetRule(ctx, id, dbInput(input))
	if err != nil {
		return Rule{}, mapDBError(err)
	}
	return s.rule(*row), nil
}

func (s dbStore) Delete(ctx context.Context, key string) error {
	id, err := uuid.Parse(key)
	if err != nil {
		return fmt.Errorf("%w: invalid database key %q", ErrNotFound, key)
	}
	db, err := s.writer(ctx)
	if err != nil {
		return err
	}
	return mapDBError(db.DeleteBudgetRule(ctx, id))
}

func (s dbStore) writer(ctx context.Context) (*database.DB, error) {
	if s.options.Write == nil {
		return nil, fmt.Errorf("%w: %s", ErrReadOnly, s.info.Label)
	}
	return s.options.Write(ctx)
}

func (s dbStore) rule(row database.BudgetRule) Rule {
	key := row.ID.String()
	return Rule{ID: encodeID(s.info.ID, key), Key: key, Source: s.info, Name: row.Name,
		Match: RuleMatch{Dimensions: row.Match.Dimensions, Models: row.Match.Models}, GroupBy: row.GroupBy,
		Amount: row.Amount, Window: row.Window, UpdatedAt: row.UpdatedAt}
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
