package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("latest transcript plan search", func() {
	It("pages bounded session summaries until the newest transcript plan", func(ctx SpecContext) {
		home := GinkgoT().TempDir()
		project := filepath.Join(home, "work", "captain")
		planPath := filepath.Join(home, ".claude", "plans", "paged-plan.md")
		historyPath := filepath.Join(home, ".claude", "projects", "captain", "session-with-plan.jsonl")
		entry, err := json.Marshal(map[string]any{
			"type": "assistant", "sessionId": "session-with-plan", "uuid": "assistant-1",
			"timestamp": "2026-07-16T10:00:00Z", "cwd": project, "slug": "paged-plan",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tool-1", "name": "ExitPlanMode",
					"input": map[string]any{"planFilePath": planPath, "plan": "# paged plan"}},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(historyPath), 0o755)).To(Succeed())
		Expect(os.WriteFile(historyPath, append(entry, '\n'), 0o644)).To(Succeed())
		missingPath := filepath.Join(home, ".claude", "projects", "missing.jsonl")
		providerID := "session-with-plan"
		store := &planListStore{pages: map[string]database.SessionListPage{
			"": {
				Rows:       []database.SessionListSummary{{ID: uuid.New(), Source: "claude", Path: &missingPath}},
				NextCursor: "second-page",
			},
			"second-page": {
				Rows: []database.SessionListSummary{{
					ID: uuid.New(), ProviderSessionID: &providerID, Source: "claude", Path: &historyPath,
				}},
				NextCursor: "unused-third-page",
			},
		}}

		plan, err := resolveLatestTranscriptPlan(ctx, store, latestTranscriptPlanQuery{
			Source: "claude", ProjectRoot: project,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(plan).NotTo(BeNil())
		Expect(*plan).To(MatchFields(IgnoreExtras, Fields{
			"SessionID": Equal(providerID),
			"Source":    Equal("claude"),
			"Path":      Equal(planPath),
			"Content":   Equal("# paged plan"),
		}))
		Expect(store.filters).To(HaveLen(2))
		Expect(store.filters[0]).To(MatchFields(IgnoreExtras, Fields{
			"Source":      Equal("claude"),
			"ProjectRoot": Equal(project),
			"RootsOnly":   BeTrue(),
			"Limit":       Equal(latestTranscriptPlanPageLimit),
			"Cursor":      BeEmpty(),
		}))
		Expect(store.filters[1].Cursor).To(Equal("second-page"))
	})
})

type planListStore struct {
	pages   map[string]database.SessionListPage
	filters []database.SessionListFilter
}

func (s *planListStore) ListSessionSummaries(
	_ context.Context,
	filter database.SessionListFilter,
) (database.SessionListPage, error) {
	s.filters = append(s.filters, filter)
	return s.pages[filter.Cursor], nil
}

func (*planListStore) ListPlans(context.Context, database.PlanFilter) ([]database.Plan, error) {
	return nil, nil
}
