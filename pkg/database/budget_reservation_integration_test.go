package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestReserveChatTurnBudgetsAgainstSettledLedger classifies reservation
// outcomes against real settled spend: an exhausted bucket is a capacity
// error, a non-USD ledger is ErrBudgetSpendNotUSD, and a failed query is
// neither.
func TestReserveChatTurnBudgetsAgainstSettledLedger(t *testing.T) {
	ctx := t.Context()
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_budget_reservation"})
	db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	usdRule, err := db.CreateBudgetRule(ctx, BudgetRuleInput{Name: "usd-hourly", Amount: 0.01, Window: "now-1h"})
	require.NoError(t, err)
	eurRule, err := db.CreateBudgetRule(ctx, BudgetRuleInput{Name: "eur-hourly", Amount: 0.01, Window: "now-1h"})
	require.NoError(t, err)

	reserve := func(ctx context.Context, rule *BudgetRule) (uuid.UUID, error) {
		turnID := ingestLedgerTurn(t, db, nil)
		return turnID, db.ReserveChatTurnBudgets(ctx, turnID, []BudgetReservation{{
			RuleID: rule.ID, Rule: rule.Name, GroupValues: map[string]string{},
			Amount: 0.001, Limit: rule.Amount, WindowStart: time.Now().UTC().Add(-time.Hour),
		}})
	}
	var capacity *BudgetCapacityError

	settle(t, db, usdRule.ID, "USD", 0.004)
	held, err := reserve(ctx, usdRule)
	require.NoError(t, err, "settled spend plus the hold below the limit reserves")
	require.NoError(t, db.ReleaseChatTurnBudgetReservations(ctx, held))

	settle(t, db, usdRule.ID, "USD", 0.006)
	_, err = reserve(ctx, usdRule)
	require.ErrorAs(t, err, &capacity, "settled spend at the limit leaves no room for a hold")
	require.InDelta(t, 0.011, capacity.Committed, 1e-9)

	settle(t, db, eurRule.ID, "EUR", 0.001)
	_, err = reserve(ctx, eurRule)
	require.ErrorIs(t, err, ErrBudgetSpendNotUSD, "a non-USD ledger fails closed")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = reserve(cancelled, usdRule)
	require.Error(t, err)
	require.False(t, errors.As(err, &capacity) || errors.Is(err, ErrBudgetSpendNotUSD),
		"a failed query is operational, not a budget decision: %v", err)
}

// settle ingests one completed model call and attributes it to rule, the same
// ledger shape chat admission writes.
func settle(t *testing.T, db *DB, ruleID uuid.UUID, currency string, cost float64) {
	t.Helper()
	ended := time.Now().UTC()
	started := ended.Add(-time.Second)
	turnID := ingestLedgerTurn(t, db, &IngestModelCall{
		Model: "claude-sonnet-5", Provider: "anthropic", InputCost: cost, Currency: currency,
		StartedAt: &started, EndedAt: &ended,
	})
	var call struct{ ID uuid.UUID }
	require.NoError(t, db.Gorm().WithContext(t.Context()).Raw(
		`SELECT id FROM captain_model_calls WHERE turn_id = ?`, turnID,
	).Scan(&call).Error)
	require.NotEqual(t, uuid.Nil, call.ID)
	require.NoError(t, db.SetModelCallBudgets(t.Context(), turnID, call.ID, nil,
		[]BudgetAttribution{{RuleID: ruleID, GroupValues: map[string]string{}}}))
}

// ingestLedgerTurn ingests a one-turn transcript, with call when given, and
// returns the turn's ID.
func ingestLedgerTurn(t *testing.T, db *DB, call *IngestModelCall) uuid.UUID {
	t.Helper()
	ended := time.Now().UTC()
	started := ended.Add(-time.Second)
	providerSessionID := uuid.NewString()
	session, err := db.IngestTranscript(t.Context(), IngestTranscriptInput{
		Session: IngestSessionInput{ProviderSessionID: providerSessionID, Source: "claude", HostID: "budget-test"},
		Source: IngestSourceInput{
			SourceKind: "claude", Path: "/budget-test/" + providerSessionID + ".jsonl", ParserVersion: 1,
		},
		Turns: []IngestTurn{{Index: 0, Status: TurnStatusEnded, StartedAt: &started, EndedAt: &ended, Call: call}},
	})
	require.NoError(t, err)
	var turn struct{ ID uuid.UUID }
	require.NoError(t, db.Gorm().WithContext(t.Context()).Raw(
		`SELECT id FROM captain_turns WHERE session_id = ?`, session.ID,
	).Scan(&turn).Error)
	require.NotEqual(t, uuid.Nil, turn.ID)
	return turn.ID
}
