package approval_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

// gavelCase is one Gavel v0.0.63 dashboard interaction, checked in as
// testdata/gavel-v0.0.63.json: the row as its approval query lists it, the body
// its approve endpoint receives, and the decision the waiting provider gets.
type gavelCase struct {
	Name    string `json:"name"`
	Request string `json:"request"`
	Listed  struct {
		Tool  string         `json:"tool"`
		Input map[string]any `json:"input"`
	} `json:"listed"`
	Body struct {
		Action  string         `json:"action"`
		Message string         `json:"message"`
		Input   map[string]any `json:"input"`
	} `json:"body"`
	Decision api.ApprovalDecision `json:"decision"`
}

func loadGavelCases() []gavelCase {
	raw, err := os.ReadFile("testdata/gavel-v0.0.63.json")
	if err != nil {
		panic(err)
	}
	var fixture struct {
		Cases []gavelCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		panic(err)
	}
	return fixture.Cases
}

// gavelListed reads a pending row the way Gavel v0.0.63's todoApprovalOf does:
// only the request's "tool" and "input" keys.
func gavelListed(ctx context.Context, db *database.DB, run *providerRun) (string, map[string]any) {
	GinkgoHelper()
	rows, err := db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: run.session, PromptRunID: &run.run})
	Expect(err).NotTo(HaveOccurred())
	Expect(rows).To(HaveLen(1))
	tool, _ := rows[0].Request["tool"].(string)
	input, _ := rows[0].Request["input"].(map[string]any)
	return tool, input
}

var _ = Describe("Gavel v0.0.63 against typed approval requests", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	requests := map[string]api.ApprovalRequest{
		"command": {
			Tool: "exec_command", Kind: api.ApprovalKindCommand, ToolUseID: "appr_1",
			Input:   map[string]any{"command": "git rebase --continue"},
			Command: &api.CommandApproval{Command: "git rebase --continue"},
		},
		"question": {
			Tool: "AskUserQuestion", Kind: api.ApprovalKindQuestion, ToolUseID: "item_q",
			Input:     map[string]any{"questions": []any{map[string]any{"id": "scope", "question": "How far?"}}},
			Questions: []api.TerminalQuestion{{ID: "scope", Text: "How far?", MultiSelect: true}},
		},
		"plan": {
			Tool: "ExitPlanMode", Kind: api.ApprovalKindPlan, ToolUseID: "toolu_plan", Interruptible: true,
			Input: map[string]any{"plan": "1. Add the plan kind", "planFilePath": "/repo/.claude/plans/p.md"},
			Plan:  &api.TerminalPlan{Content: "1. Add the plan kind", Path: "/repo/.claude/plans/p.md"},
		},
		"elicitation": {
			Tool: "Elicitation", Kind: api.ApprovalKindElicitation, ToolUseID: "elicit:7", Interruptible: true,
			Input: map[string]any{"serverName": "linear", "message": "Pick a team"},
			Elicitation: &api.ElicitationApproval{
				Server: "linear", Mode: api.ElicitationModeForm, Message: "Pick a team",
				Schema: map[string]any{"type": "object", "properties": map[string]any{"team": map[string]any{"type": "string"}}},
			},
		},
	}

	for _, gavel := range loadGavelCases() {
		It(gavel.Name, func(ctx SpecContext) {
			request, ok := requests[gavel.Request]
			Expect(ok).To(BeTrue(), "fixture names an unknown request %q", gavel.Request)
			run := newProviderRun(ctx, db)
			outcomes := run.callTool(ctx, run.broker(time.Minute), request)
			event := run.awaitPermission()

			By("reading the event and the listed row through the old fields only")
			Expect(event.Tool).To(Equal(gavel.Listed.Tool))
			Expect(event.Input).To(Equal(gavel.Listed.Input))
			tool, input := gavelListed(ctx, db, run)
			Expect(tool).To(Equal(gavel.Listed.Tool))
			Expect(input).To(Equal(gavel.Listed.Input))

			By("resolving the way the approve endpoint does")
			_, err := approval.Resolve(ctx, db, approval.ResolveInput{
				RequestID: uuid.MustParse(event.ApprovalID), SessionID: run.session,
				Approved: gavel.Body.Action != "deny", ResolvedBy: "gavel-dashboard",
				Reason: strings.TrimSpace(gavel.Body.Message), UpdatedInput: gavel.Body.Input,
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(outcomes).Should(Receive(Equal(outcome{decision: gavel.Decision})))
		})
	}
})
