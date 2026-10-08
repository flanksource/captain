package query_test

import (
	"context"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session/query"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type foldStoreStub struct {
	byIdentity map[string][]database.SessionOverview
	children   []database.SessionOverview
	childCalls [][]uuid.UUID
}

func (s *foldStoreStub) ListSessionOverviewsByIdentity(_ context.Context, identity string) ([]database.SessionOverview, error) {
	rows, ok := s.byIdentity[identity]
	if !ok {
		return nil, database.ErrSessionNotFound
	}
	return rows, nil
}

func (s *foldStoreStub) ListThreadSessionOverviews(context.Context, uuid.UUID) ([]database.SessionOverview, error) {
	return nil, nil
}

func (s *foldStoreStub) ListTranscriptChildOverviews(_ context.Context, parentIDs []uuid.UUID) ([]database.SessionOverview, error) {
	s.childCalls = append(s.childCalls, parentIDs)
	var rows []database.SessionOverview
	for _, child := range s.children {
		for _, parentID := range parentIDs {
			if child.ParentSessionID != nil && *child.ParentSessionID == parentID {
				rows = append(rows, child)
			}
		}
	}
	return rows, nil
}

var _ = Describe("FoldTranscripts", func() {
	var (
		todoID, runID, transcriptID uuid.UUID
		providerID                  string
		todo, run, transcript       database.SessionOverview
	)

	BeforeEach(func() {
		todoID, runID, transcriptID = uuid.New(), uuid.New(), uuid.New()
		providerID = "6c8440dd-5fad-43c5-b8f8-8047940ca5e5"
		todo = database.SessionOverview{ID: todoID, Source: "gavel", Provider: "todos"}
		run = database.SessionOverview{
			ID: runID, Source: "gavel", Provider: "agent-claude", ProviderSessionID: &providerID,
			ParentSessionID: &todoID, RootSessionID: &todoID, ParentRelation: database.SessionParentRelationAgent,
		}
		transcript = database.SessionOverview{
			ID: transcriptID, Source: "claude", ProviderSessionID: &providerID,
			ParentSessionID: &runID, RootSessionID: &todoID, ParentRelation: database.SessionParentRelationTranscript,
			ToolCallCount: 11,
		}
	})

	It("folds a transcript child into its parent when a provider id matches both", func(ctx SpecContext) {
		store := &foldStoreStub{children: []database.SessionOverview{transcript}}

		folded, err := query.FoldTranscripts(ctx, store, []database.SessionOverview{transcript, run})

		Expect(err).NotTo(HaveOccurred())
		Expect(folded).To(Equal([]query.FoldedOverview{{Session: run, Transcript: &transcript}}))
	})

	It("loads the parent of a transcript child resolved on its own", func(ctx SpecContext) {
		store := &foldStoreStub{
			byIdentity: map[string][]database.SessionOverview{runID.String(): {run}},
			children:   []database.SessionOverview{transcript},
		}

		folded, err := query.FoldTranscripts(ctx, store, []database.SessionOverview{transcript})

		Expect(err).NotTo(HaveOccurred())
		Expect(folded).To(Equal([]query.FoldedOverview{{Session: run, Transcript: &transcript}}))
	})

	It("attaches the transcript child of a parent resolved without it", func(ctx SpecContext) {
		store := &foldStoreStub{children: []database.SessionOverview{transcript}}

		folded, err := query.FoldTranscripts(ctx, store, []database.SessionOverview{todo, run})

		Expect(err).NotTo(HaveOccurred())
		Expect(folded).To(Equal([]query.FoldedOverview{
			{Session: todo},
			{Session: run, Transcript: &transcript},
		}))
		Expect(store.childCalls).To(Equal([][]uuid.UUID{{todoID, runID}}))
	})

	It("leaves sessions without a transcript child unchanged", func(ctx SpecContext) {
		store := &foldStoreStub{}

		folded, err := query.FoldTranscripts(ctx, store, []database.SessionOverview{todo})

		Expect(err).NotTo(HaveOccurred())
		Expect(folded).To(Equal([]query.FoldedOverview{{Session: todo}}))
	})

	It("refuses a parent with more than one transcript child", func(ctx SpecContext) {
		second := transcript
		second.ID = uuid.New()
		store := &foldStoreStub{children: []database.SessionOverview{transcript, second}}

		_, err := query.FoldTranscripts(ctx, store, []database.SessionOverview{run})

		Expect(err).To(MatchError(database.ErrSessionConflict))
		Expect(err).To(MatchError(ContainSubstring(runID.String())))
	})

	It("refuses a second transcript child that only the child lookup finds", func(ctx SpecContext) {
		second := transcript
		second.ID = uuid.New()
		store := &foldStoreStub{children: []database.SessionOverview{transcript, second}}

		_, err := query.FoldTranscripts(ctx, store, []database.SessionOverview{transcript, run})

		Expect(err).To(MatchError(database.ErrSessionConflict))
	})

	It("re-parents a sub-agent of the transcript row onto the session it folded into", func(ctx SpecContext) {
		subAgent := database.SessionOverview{
			ID: uuid.New(), Source: "claude", ParentSessionID: &transcriptID, RootSessionID: &todoID,
			ParentRelation: database.SessionParentRelationAgent,
		}
		store := &foldStoreStub{children: []database.SessionOverview{transcript}}

		folded, err := query.FoldTranscripts(ctx, store, []database.SessionOverview{todo, run, transcript, subAgent})

		Expect(err).NotTo(HaveOccurred())
		Expect(folded).To(HaveLen(3))
		Expect(folded[2].Session.ID).To(Equal(subAgent.ID))
		Expect(folded[2].Session.ParentSessionID).To(HaveValue(Equal(runID)))
	})

	It("fails when a transcript child's parent cannot be loaded", func(ctx SpecContext) {
		store := &foldStoreStub{byIdentity: map[string][]database.SessionOverview{}}

		_, err := query.FoldTranscripts(ctx, store, []database.SessionOverview{transcript})

		Expect(err).To(MatchError(database.ErrSessionNotFound))
	})
})
