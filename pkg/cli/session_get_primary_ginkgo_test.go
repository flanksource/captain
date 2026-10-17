package cli

import (
	"context"

	"github.com/flanksource/captain/pkg/database"
	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// GET /api/captain/sessions/{id} serializes only the session the id names, but
// a root id expands to its whole thread: hydrating every member (and probing
// the process table for browsers the response never carries) cost seconds on
// threads with a dozen sub-agents.
var _ = Describe("session get primary", func() {
	rootID := uuid.MustParse("5c1f0a52-8f0e-4c55-9d0b-0b9e2a7c1a01")
	thread := func() []database.SessionOverview {
		rows := []database.SessionOverview{{ID: rootID, Source: "captain"}}
		for _, id := range []string{
			"5c1f0a52-8f0e-4c55-9d0b-0b9e2a7c1a02",
			"5c1f0a52-8f0e-4c55-9d0b-0b9e2a7c1a03",
			"5c1f0a52-8f0e-4c55-9d0b-0b9e2a7c1a04",
		} {
			rows = append(rows, database.SessionOverview{
				ID: uuid.MustParse(id), ParentSessionID: &rootID, RootSessionID: &rootID, Source: "captain",
				ParentRelation: database.SessionParentRelationAgent,
			})
		}
		return rows
	}

	It("hydrates only the root a thread id names, and probes no browsers", func() {
		rows := thread()
		store := &sessionGetOverviewStore{identity: rows[:1], thread: rows}
		ctx, timings := rpchttp.WithTimings(context.Background())

		result, err := runSessionGet(ctx, store, SessionGetOptions{ID: rootID.String(), Primary: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(1))
		Expect(result.Sessions[0].CaptainID).To(Equal(rootID.String()))
		Expect(store.promptRunSessions).To(Equal([]uuid.UUID{rootID}))
		Expect(timings.Header()).NotTo(ContainSubstring("browsers"))
	})

	It("still hydrates the whole thread without Primary", func() {
		rows := thread()
		store := &sessionGetOverviewStore{identity: rows[:1], thread: rows}

		result, err := runSessionGet(context.Background(), store, SessionGetOptions{ID: rootID.String()})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(len(rows)))
		Expect(store.promptRunSessions).To(HaveLen(len(rows)))
	})

	It("refuses an id naming unrelated sessions as a conflict", func() {
		first, second := uuid.New(), uuid.New()
		store := &sessionGetOverviewStore{identity: []database.SessionOverview{
			{ID: first, Source: "claude", ParentSessionID: &rootID},
			{ID: second, Source: "codex", ParentSessionID: &rootID},
		}}

		_, err := runSessionGet(context.Background(), store, SessionGetOptions{ID: "shared-prefix", Primary: true})

		Expect(err).To(MatchError(database.ErrSessionConflict))
		Expect(sessionErrorStatus(err)).To(Equal(409))
		Expect(store.promptRunSessions).To(BeEmpty(), "nothing is hydrated before the conflict is found")
	})
})
