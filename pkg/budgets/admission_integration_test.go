package budgets_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/budgets"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestAdmitAgainstSettledLedger classifies admission outcomes against real
// settled spend: exhausted and non-USD ledgers are budget refusals, while a
// failed spend query stays an operational error.
func TestAdmitAgainstSettledLedger(t *testing.T) {
	ctx := t.Context()
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_budget_admission"})
	db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	catalog, err := budgets.NewCatalog(budgets.CatalogOptions{
		Read: func(context.Context) (*database.DB, error) { return db, nil },
	})
	require.NoError(t, err)
	usdRule, err := db.CreateBudgetRule(ctx, database.BudgetRuleInput{Name: "usd-hourly", Amount: 0.01, Window: "now-1h"})
	require.NoError(t, err)
	eurRule, err := db.CreateBudgetRule(ctx, database.BudgetRuleInput{Name: "eur-hourly", Amount: 0.01, Window: "now-1h"})
	require.NoError(t, err)
	rules, err := catalog.List(ctx)
	require.NoError(t, err)
	ruleNamed := func(name string) []budgets.Rule {
		for _, rule := range rules {
			if rule.Name == name {
				return []budgets.Rule{rule}
			}
		}
		t.Fatalf("budget rule %q not listed", name)
		return nil
	}

	models := []api.Model{{Name: "claude-sonnet-5"}}
	now := time.Now().UTC()
	admit := func(ctx context.Context, rules []budgets.Rule) error {
		_, err := budgets.Admit(ctx, rules, nil, models, api.Budget{Cost: 1}, now, db)
		return err
	}
	var refusal *budgets.Refusal

	_, err = budgets.Admit(ctx, ruleNamed("usd-hourly"), nil, models, api.Budget{}, now, db)
	require.ErrorAs(t, err, &refusal, "active rules without a per-run budget cost refuse")

	settle(t, db, usdRule.ID, "USD", 0.004)
	require.NoError(t, admit(ctx, ruleNamed("usd-hourly")), "settled spend below the amount admits")

	settle(t, db, usdRule.ID, "USD", 0.006)
	err = admit(ctx, ruleNamed("usd-hourly"))
	require.ErrorAs(t, err, &refusal, "settled spend at the amount refuses")
	require.InDelta(t, 0.01, refusal.Spend, 1e-9)

	settle(t, db, eurRule.ID, "EUR", 0.001)
	err = admit(ctx, ruleNamed("eur-hourly"))
	require.ErrorAs(t, err, &refusal, "a non-USD ledger fails closed as a refusal")
	require.Contains(t, refusal.Reason, database.ErrBudgetSpendNotUSD.Error())

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err = admit(cancelled, ruleNamed("usd-hourly"))
	require.Error(t, err)
	require.False(t, errors.As(err, &refusal), "a failed spend query is operational, not a refusal: %v", err)
}

// settle ingests one completed model call and attributes it to rule, the same
// ledger shape chat admission writes.
func settle(t *testing.T, db *database.DB, ruleID uuid.UUID, currency string, cost float64) {
	t.Helper()
	ctx := t.Context()
	ended := time.Now().UTC()
	started := ended.Add(-time.Second)
	providerSessionID := uuid.NewString()
	session, err := db.IngestTranscript(ctx, database.IngestTranscriptInput{
		Session: database.IngestSessionInput{ProviderSessionID: providerSessionID, Source: "claude", HostID: "budget-test"},
		Source: database.IngestSourceInput{
			SourceKind: "claude", Path: "/budget-test/" + providerSessionID + ".jsonl", ParserVersion: 1,
		},
		Turns: []database.IngestTurn{{
			Index: 0, Status: database.TurnStatusEnded, StartedAt: &started, EndedAt: &ended,
			Call: &database.IngestModelCall{
				Model: "claude-sonnet-5", Provider: "anthropic", InputCost: cost, Currency: currency,
				StartedAt: &started, EndedAt: &ended,
			},
		}},
	})
	require.NoError(t, err)
	var call struct {
		ID     uuid.UUID
		TurnID uuid.UUID
	}
	require.NoError(t, db.Gorm().WithContext(ctx).Raw(
		`SELECT calls.id, calls.turn_id FROM captain_model_calls AS calls
		 JOIN captain_turns AS turns ON turns.id = calls.turn_id WHERE turns.session_id = ?`, session.ID,
	).Scan(&call).Error)
	require.NotEqual(t, uuid.Nil, call.ID)
	require.NoError(t, db.SetModelCallBudgets(ctx, call.TurnID, call.ID, nil,
		[]database.BudgetAttribution{{RuleID: ruleID, GroupValues: map[string]string{}}}))
}
