package load

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/session"
)

// Tool request states, mirroring database.TurnRequestState. They are restated
// rather than imported because composition must not depend on the store; the
// unknown-state error below is what keeps the two from drifting apart quietly.
const (
	requestStatePending   = "pending"
	requestStateApproved  = "approved"
	requestStateDenied    = "denied"
	requestStateCancelled = "cancelled"
	requestStateExpired   = "expired"
)

// Requests contributes the session's tool approval requests and the counts
// derived from them. It is the only source allowed to claim FacetApprovals.
//
// Approvals used to be loaded in exactly one of three mutually exclusive
// branches, so a run suspended on a tool approval reported {approved: 0,
// denied: 0} whenever either of the other two served the request. Counting them
// here, on every load, is what makes a blocked run visible as blocked.
func Requests(requests []session.Request) Contributor {
	return requestsContributor{requests: requests}
}

type requestsContributor struct {
	requests []session.Request
}

func (c requestsContributor) Source() Source { return SourceRequests }

func (c requestsContributor) Contribute(_ context.Context, aggregate *session.Session, prov *Provenance) error {
	if len(c.requests) == 0 {
		return ErrNoFacts
	}
	if !prov.Claim(FacetApprovals, SourceRequests) {
		return nil
	}
	aggregate.Requests = c.requests
	stats, unknown := approvalStats(c.requests)
	aggregate.Approvals = stats
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("unrecognised tool request state(s) %s: counted in no approval bucket",
		strings.Join(unknown, ", "))
}

// approvalStats counts requests by state, returning the states it could not
// place. Pending, Cancelled and Expired are counted alongside Approved and
// Denied: a request that is unanswered or lapsed is a fact about the run, and
// leaving it out is what made a suspended run read as an idle one.
func approvalStats(requests []session.Request) (session.ApprovalStats, []string) {
	var stats session.ApprovalStats
	unknown := map[string]struct{}{}
	for _, request := range requests {
		switch request.State {
		case requestStateApproved:
			stats.Approved++
		case requestStateDenied:
			stats.Denied++
			stats.Denials = append(stats.Denials, session.Denial{
				ToolUseID: request.ToolCallID, Tool: request.Tool, Reason: request.Reason,
			})
		case requestStatePending:
			stats.Pending++
		case requestStateCancelled:
			stats.Cancelled++
		case requestStateExpired:
			stats.Expired++
		default:
			unknown[request.State] = struct{}{}
		}
	}
	return stats, sortedKeys(unknown)
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
