package cli

import (
	"context"

	"github.com/flanksource/captain/pkg/database"
	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("session get timing", func() {
	It("reports lookup, hydration, and prompt-run phases", func() {
		sessionID := uuid.MustParse("055781c7-360a-4eb2-80be-452b3937fcfe")
		store := &sessionGetOverviewStore{
			identity: []database.SessionOverview{{ID: sessionID, Source: "captain"}},
		}
		ctx, timings := rpchttp.WithTimings(context.Background())

		_, err := runSessionGet(ctx, store, SessionGetOptions{ID: sessionID.String()})

		Expect(err).NotTo(HaveOccurred())
		Expect(timings.Header()).To(And(
			ContainSubstring("lookup;dur="),
			ContainSubstring("hydrate;dur="),
			ContainSubstring("prompt_runs;dur="),
		))
	})
})
