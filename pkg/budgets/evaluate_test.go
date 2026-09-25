package budgets

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

type stubSpend struct {
	used float64
	err  error
}

func (s stubSpend) BudgetSpendUSD(context.Context, uuid.UUID, map[string]string, time.Time) (float64, error) {
	return s.used, s.err
}

func TestEnforceSpendErrorClassification(t *testing.T) {
	rules := []Rule{{ID: uuid.New(), Name: "daily", Amount: 10, Window: "now-1d"}}
	models := []api.Model{{Name: "claude-sonnet-5"}}
	now := time.Now().UTC()

	var refusal *Refusal
	_, err := Enforce(context.Background(), rules, nil, models, now, stubSpend{used: 10})
	if !errors.As(err, &refusal) || refusal.Spend != 10 {
		t.Fatalf("exhausted budget: want Refusal with spend 10, got %v", err)
	}

	_, err = Enforce(context.Background(), rules, nil, models, now,
		stubSpend{err: fmt.Errorf("%w: 2 calls", database.ErrBudgetSpendNotUSD)})
	if !errors.As(err, &refusal) {
		t.Fatalf("non-USD ledger: want Refusal, got %v", err)
	}

	queryErr := errors.New("connection refused")
	_, err = Enforce(context.Background(), rules, nil, models, now, stubSpend{err: queryErr})
	if errors.As(err, &refusal) || !errors.Is(err, queryErr) {
		t.Fatalf("query failure: want wrapped operational error, got %v", err)
	}

	if _, err := Enforce(context.Background(), rules, nil, models, now, stubSpend{used: 1}); err != nil {
		t.Fatalf("under budget: want admission, got %v", err)
	}
}
