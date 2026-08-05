package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// planIdentityStoreStub resolves one identity to several overviews, mirroring a
// provider session ID recorded once per source (captain_sessions is unique on
// source+host_id+provider_session_id, not on provider_session_id alone).
type planIdentityStoreStub struct {
	overviews []database.SessionOverview
	plans     map[uuid.UUID][]database.Plan
}

func (s *planIdentityStoreStub) ListSessionOverviewsByIdentity(
	context.Context, string,
) ([]database.SessionOverview, error) {
	return s.overviews, nil
}

func (s *planIdentityStoreStub) ListThreadSessionOverviews(
	context.Context, uuid.UUID,
) ([]database.SessionOverview, error) {
	return nil, nil
}

func (s *planIdentityStoreStub) ListPlans(
	_ context.Context, filter database.PlanFilter,
) ([]database.Plan, error) {
	if filter.SourceSessionID == nil {
		return nil, nil
	}
	return s.plans[*filter.SourceSessionID], nil
}

var _ = Describe("plan identity resolution", func() {
	const providerSessionID = "7657484f-e2e6-4f71-85c7-c244577a4028"

	// writeClaudePlanTranscript emits a one-entry Claude transcript whose
	// ExitPlanMode call carries the plan, and returns its path.
	writeClaudePlanTranscript := func(planMarkdown string) (string, string) {
		home := GinkgoT().TempDir()
		planPath := filepath.Join(home, ".claude", "plans", "identity-plan.md")
		historyPath := filepath.Join(home, ".claude", "projects", "identity.jsonl")
		entry, err := json.Marshal(map[string]any{
			"type": "assistant", "sessionId": providerSessionID, "uuid": "assistant-1",
			"timestamp": "2026-08-05T10:00:00Z", "cwd": home, "slug": "identity-plan",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "tool-1", "name": "ExitPlanMode",
					"input": map[string]any{"planFilePath": planPath, "plan": planMarkdown}},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.MkdirAll(filepath.Dir(historyPath), 0o755)).To(Succeed())
		Expect(os.WriteFile(historyPath, append(entry, '\n'), 0o644)).To(Succeed())
		return historyPath, planPath
	}

	It("selects the transcript-bearing source when one provider ID spans sources", func(ctx SpecContext) {
		const planMarkdown = "# identity plan"
		historyPath, planPath := writeClaudePlanTranscript(planMarkdown)
		orchestrationID, transcriptID := uuid.New(), uuid.New()
		providerID := providerSessionID
		store := &planIdentityStoreStub{overviews: []database.SessionOverview{
			// The gavel orchestration row is listed first and has no transcript.
			{ID: orchestrationID, ProviderSessionID: &providerID, Source: "gavel"},
			{ID: transcriptID, ProviderSessionID: &providerID, Source: "claude", Path: &historyPath},
		}}

		plan, err := resolveIdentityPlan(ctx, store, providerSessionID, "all")

		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Content).To(Equal(planMarkdown))
		Expect(plan.Path).To(Equal(planPath))
		Expect(plan.Source).To(Equal("claude"))
	})

	It("prefers a persisted plan over transcript recovery", func(ctx SpecContext) {
		historyPath, _ := writeClaudePlanTranscript("# transcript plan")
		orchestrationID, transcriptID := uuid.New(), uuid.New()
		providerID := providerSessionID
		planID, revisionID := uuid.New(), uuid.New()
		store := &planIdentityStoreStub{
			overviews: []database.SessionOverview{
				{ID: orchestrationID, ProviderSessionID: &providerID, Source: "gavel"},
				{ID: transcriptID, ProviderSessionID: &providerID, Source: "claude", Path: &historyPath},
			},
			plans: map[uuid.UUID][]database.Plan{orchestrationID: {{
				ID: planID, Slug: "persisted",
				ApprovedRevision: &database.PlanRevision{ID: revisionID, Revision: 2, PlanMarkdown: "# persisted plan"},
			}}},
		}

		plan, err := resolveIdentityPlan(ctx, store, providerSessionID, "all")

		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Content).To(Equal("# persisted plan"))
		Expect(plan.SessionID).To(Equal(orchestrationID.String()))
	})

	It("reports a missing transcript when no matched source recorded one", func(ctx SpecContext) {
		providerID := providerSessionID
		store := &planIdentityStoreStub{overviews: []database.SessionOverview{
			{ID: uuid.New(), ProviderSessionID: &providerID, Source: "gavel"},
			{ID: uuid.New(), ProviderSessionID: &providerID, Source: "claude"},
		}}

		_, err := resolveIdentityPlan(ctx, store, providerSessionID, "all")

		Expect(err).To(MatchError(ContainSubstring("has no transcript recorded on this host")))
		Expect(errors.Is(err, database.ErrSessionConflict)).To(BeFalse())
	})

	It("narrows matches to the requested source", func(ctx SpecContext) {
		historyPath, _ := writeClaudePlanTranscript("# identity plan")
		providerID := providerSessionID
		store := &planIdentityStoreStub{overviews: []database.SessionOverview{
			{ID: uuid.New(), ProviderSessionID: &providerID, Source: "gavel"},
			{ID: uuid.New(), ProviderSessionID: &providerID, Source: "claude", Path: &historyPath},
		}}

		_, err := resolveIdentityPlan(ctx, store, providerSessionID, "codex")

		Expect(errors.Is(err, database.ErrSessionNotFound)).To(BeTrue())
	})
})
