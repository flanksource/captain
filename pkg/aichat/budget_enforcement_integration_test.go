package aichat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/budgets"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons-db/dbtest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Budget enforcement against settled spend", func() {
	It("admits covered turns until settled spend reaches the rule amount", func(ctx SpecContext) {
		GinkgoT().Setenv(api.MonitorHooksEnv, "off")
		covered := api.Model{Name: "claude-sonnet-4-6", Mode: api.ModeAPI}
		uncovered := api.Model{Name: "claude-haiku-4-5", Mode: api.ModeAPI}
		runBudget := api.Budget{Cost: 1}

		mock := startLifecycleMock(lifecycleRuntime{protocol: aimock.SectionAnthropic, scenario: "chat-api-flows.yaml"})
		DeferCleanup(mock.server.Close)
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_budget_enforcement"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		// Any settled call exceeds this amount, so the first completed model call
		// exhausts the rule.
		rule, err := db.CreateBudgetRule(ctx, database.BudgetRuleInput{
			Name: "sonnet-hourly", Match: database.BudgetRuleMatch{Models: []string{covered.Name}},
			Amount: 0.00001, Window: "now-1h",
		})
		Expect(err).NotTo(HaveOccurred())

		// Wired exactly as captain serve wires it.
		catalog, err := budgets.NewCatalog(budgets.CatalogOptions{
			Read: func(context.Context) (*database.DB, error) { return db, nil },
		})
		Expect(err).NotTo(HaveOccurred())
		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		authority, err := aichat.NewDatabaseExecutionAuthority(db, aichat.WithBudgetCatalog(catalog))
		Expect(err).NotTo(HaveOccurred())
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: realChatResolver{}, Threads: aichat.FixedThreadStore(store), Authority: authority,
			Profile: aichat.RuntimeProfileProviderFunc(func(context.Context, ...aichat.RuntimeProfileOption) (aichat.RuntimeProfile, error) {
				return aichat.RuntimeProfile{ProviderConfig: api.Config{APIURL: mock.apiURL, APIKey: aimock.DummyKey}}, nil
			}),
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "accounts_edit", DefaultPermission: api.ToolPolicyAsk,
				Handler: func(context.Context, map[string]any) (any, error) {
					Fail("a budget-refused approval must not execute its tool")
					return nil, nil
				},
			}}),
		})
		server := httptest.NewServer(service.Handler())
		DeferCleanup(server.Close)
		client := server.Client()

		chat := func(threadID string, model api.Model, budget api.Budget, prompt string) httpResult {
			return postLifecycleJSON(ctx, client, http.MethodPost, server.URL+"/api/chat", aichat.ChatRequest{
				ID: threadID, ThreadID: threadID, Trigger: "submit-message", Runtime: &model, Budget: budget,
				Messages: []aichat.UIMessage{{
					ID: "user-" + prompt, Role: string(api.RoleUser), Parts: []aichat.UIPart{{Type: "text", Text: prompt}},
				}},
			})
		}
		// providerTurns lists the prompts that reached the provider as model calls,
		// ignoring metadata requests such as model listing.
		providerTurns := func() []string {
			var prompts []string
			for _, request := range mock.server.Requests() {
				if request.Path == "/v1/messages" {
					prompts = append(prompts, request.Request.LastUserText())
				}
			}
			return prompts
		}
		expectRefused := func(result httpResult, reason string) {
			GinkgoHelper()
			Expect(result.err).NotTo(HaveOccurred())
			Expect(result.status).To(Equal(http.StatusPaymentRequired), string(result.body))
			Expect(string(result.body)).To(ContainSubstring(reason))
		}

		By("refusing thread-less turns, whose spend cannot be attributed")
		expectRefused(chat("", covered, runBudget, "Return the lifecycle greeting"), "must carry a threadId")

		By("refusing turns without a positive per-run cost ceiling")
		thread := createLifecycleSession(ctx, client, server.URL, "No ceiling")
		expectRefused(chat(thread.ID, covered, api.Budget{}, "Return the lifecycle greeting"), "positive resolved per-run budget cost")

		By("refusing models no rule covers")
		thread = createLifecycleSession(ctx, client, server.URL, "Uncovered")
		expectRefused(chat(thread.ID, uncovered, runBudget, "Return the lifecycle greeting"), "no budget rule covers model")
		Expect(providerTurns()).To(BeEmpty(), "refused turns must never reach the provider")

		By("admitting a covered turn, which settles attributed spend while its tool approval is pending")
		approvalThread := createLifecycleSession(ctx, client, server.URL, "Approve")
		approvalCtx, cancelApprovalChat := context.WithCancel(ctx)
		DeferCleanup(cancelApprovalChat)
		approvalChat := make(chan httpResult, 1)
		go func() {
			approvalChat <- postLifecycleJSON(approvalCtx, client, http.MethodPost, server.URL+"/api/chat", aichat.ChatRequest{
				ID: approvalThread.ID, ThreadID: approvalThread.ID, Trigger: "submit-message", Runtime: &covered, Budget: runBudget,
				Messages: []aichat.UIMessage{{
					ID: "user-approve", Role: string(api.RoleUser),
					Parts: []aichat.UIPart{{Type: "text", Text: "Approve the account update"}},
				}},
			})
		}()
		var pending session.Session
		Eventually(func(g Gomega) {
			pending = getLifecycleSession(ctx, client, server.URL, approvalThread.ID)
			g.Expect(pending.Requests).To(HaveLen(1))
			g.Expect(pending.Requests[0].State).To(Equal(string(database.TurnRequestStatePending)))
			spent, err := db.BudgetSpendUSD(ctx, rule.ID, map[string]string{}, time.Now().Add(-time.Hour))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(spent).To(BeNumerically(">=", rule.Amount))
		}).WithTimeout(30 * time.Second).Should(Succeed())

		By("refusing new turns once settled spend reaches the rule amount")
		thread = createLifecycleSession(ctx, client, server.URL, "Exhausted")
		expectRefused(chat(thread.ID, covered, runBudget, "Return the lifecycle greeting"), `budget rule "sonnet-hourly"`)

		By("refusing to continue the pending approval, leaving it unresolved")
		decision := postLifecycleJSON(ctx, client, http.MethodPost,
			server.URL+"/api/chat/sessions/"+approvalThread.ID+"/approvals/"+pending.Requests[0].ID,
			map[string]any{"approved": true, "reason": "approved over budget"})
		expectRefused(decision, `budget rule "sonnet-hourly"`)
		stuck := getLifecycleSession(ctx, client, server.URL, approvalThread.ID)
		Expect(stuck.Requests).To(HaveLen(1))
		Expect(stuck.Requests[0].State).To(Equal(string(database.TurnRequestStatePending)))

		Expect(providerTurns()).To(Equal([]string{"Approve the account update"}),
			"only the admitted turn may reach the provider: %s", lifecycleRequestsJSON(mock.server.Requests()))
	})
})
