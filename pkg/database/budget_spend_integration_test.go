// Specs for a budget rule's spend from the model-call ledger: the calls attributed to one of its
// groups since a time, priced by ModelCallCostUSD, and refused when any of them isn't in USD.
package database

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Budget spend", func() {
	const schema = "agent_budget_spend_context"
	var (
		db   *DB
		rule *BudgetRule
	)

	BeforeEach(func(ctx SpecContext) {
		db = openSchemaScopedDatabases(ctx, "captain_budget_spend", schema)[0]
		var err error
		rule, err = db.CreateBudgetRule(ctx, BudgetRuleInput{Name: "per org", GroupBy: []string{"org"}, Amount: 10, Window: "now-30d"})
		Expect(err).NotTo(HaveOccurred())
	})

	attribute := func(ctx context.Context, callID uuid.UUID, org string) {
		var turnID string
		Expect(db.Gorm().Table(schema+".captain_model_calls").Where("id = ?", callID).Select("turn_id").Scan(&turnID).Error).To(Succeed())
		Expect(db.SetModelCallBudgets(ctx, uuid.MustParse(turnID), callID, map[string]string{"org": org},
			[]BudgetAttribution{{RuleID: rule.ID, GroupValues: map[string]string{"org": org}}})).To(Succeed())
	}

	It("totals the calls attributed to a group since a time", func(ctx SpecContext) {
		since := time.Now().Add(-time.Hour)
		attribute(ctx, recordModelCall(ctx, db, "acme provider priced", api.Usage{InputTokens: 10}, &api.Cost{InputCost: 1}, 0.04), "acme")
		attribute(ctx, recordModelCall(ctx, db, "acme list priced", api.Usage{InputTokens: 10}, &api.Cost{InputCost: 0.01, OutputCost: 0.02}, 0), "acme")
		attribute(ctx, recordModelCall(ctx, db, "globex", api.Usage{InputTokens: 10}, &api.Cost{InputCost: 5}, 0), "globex")

		spend, err := db.BudgetSpendUSD(ctx, rule.ID, map[string]string{"org": "acme"}, since)
		Expect(err).NotTo(HaveOccurred())
		Expect(spend).To(BeNumerically("~", 0.07, 0.000000001))

		later, err := db.BudgetSpendUSD(ctx, rule.ID, map[string]string{"org": "acme"}, time.Now().Add(time.Hour))
		Expect(err).NotTo(HaveOccurred())
		Expect(later).To(BeZero())
	})

	It("refuses a group holding a call outside USD", func(ctx SpecContext) {
		inEuros := recordModelCall(ctx, db, "acme in euros", api.Usage{InputTokens: 10}, &api.Cost{InputCost: 5}, 0)
		Expect(db.Gorm().Table(schema+".captain_model_calls").Where("id = ?", inEuros).Update("currency", "EUR").Error).To(Succeed())
		attribute(ctx, inEuros, "acme")

		_, err := db.BudgetSpendUSD(ctx, rule.ID, map[string]string{"org": "acme"}, time.Now().Add(-time.Hour))
		Expect(err).To(MatchError("budget ledger contains 1 completed non-USD model calls"))
	})
})
