package load

import "github.com/flanksource/captain/pkg/session"

// Reconcile stamps the authoritative request states onto the tool parts that
// carry a durable approval id.
//
// This is the one cross-facet write in composition: it reads approvals and
// writes messages, so it belongs to neither contributor and runs after all of
// them. Load calls it, deliberately — when the database branch did this inline
// beside the approval counts, the other two branches simply did not, and a
// caller had no way to know which behaviour it had got.
//
// A part whose request is absent is left exactly as it was: the transcript's own
// account of a tool call is not something an unrelated request may overwrite.
func Reconcile(aggregate *session.Session, requests []session.Request) {
	if aggregate == nil || len(requests) == 0 {
		return
	}
	byID := make(map[string]session.Request, len(requests))
	for _, request := range requests {
		byID[request.ID] = request
	}
	for i := range aggregate.Messages {
		for j := range aggregate.Messages[i].Parts {
			reconcilePart(&aggregate.Messages[i].Parts[j], byID)
		}
	}
}

func reconcilePart(part *session.Part, byID map[string]session.Request) {
	if part.Approval == nil {
		return
	}
	request, ok := byID[part.Approval.ID]
	if !ok {
		return
	}
	switch request.State {
	case requestStatePending:
		part.State = session.ToolStateApprovalRequested
	case requestStateApproved:
		approved := true
		if part.State == session.ToolStateApprovalRequested || part.State == session.ToolStateApprovalResponded {
			part.State = session.ToolStateApprovalResponded
		}
		part.Approval.Approved = &approved
		part.Approval.Reason = request.Reason
	case requestStateDenied, requestStateCancelled:
		approved := false
		part.State = session.ToolStateOutputDenied
		part.Approval.Approved = &approved
		part.Approval.Reason = request.Reason
	}
}
