package api

import (
	"fmt"
	"strings"
	"time"
)

// MergeReports rolls one verification round's reports into a single report.
//
// A round runs every verifier the workflow declares — `commands` then `fixture`,
// say — and each returns its own report. Keeping the last one threw the rest
// away: the round's row and its result_json.verify carried the fixture's tree
// and nothing else, and the run's summary counted half of what actually ran.
//
// The merged shape keeps each report whole and addressable: one group node per
// report, named after the report and framed by its kind, holding that report's
// tests as children and its summary as the group's own (see VerifyNode.Summary,
// which is what makes the counts add up without re-walking the children). The
// checklists concatenate, the summaries total, and only a round in which every
// report passed passes.
//
// It is an error rather than a silent choice when the reports disagree about the
// turn they judged: two turns' verdicts are not one verdict, and picking one of
// the iteration numbers is how a turn-2 failure gets filed under turn 1.
func MergeReports(name string, reports ...VerifyReport) (VerifyReport, error) {
	if len(reports) == 0 {
		return VerifyReport{}, fmt.Errorf("merge verify reports %q: no reports to merge", name)
	}
	iteration, err := sharedIteration(name, reports)
	if err != nil {
		return VerifyReport{}, err
	}

	merged := VerifyReport{Kind: mergedKind(reports), Name: name, Iteration: iteration, Passed: true, Ran: true}
	var reasons, feedback []string
	for _, r := range reports {
		merged.Tests = append(merged.Tests, reportNode(r))
		merged.Checklist = append(merged.Checklist, r.Checklist...)
		merged.Summary = AddSummaries(merged.Summary, r.Summary)
		merged.Duration += r.Duration
		merged.Passed = merged.Passed && r.Passed
		merged.Ran = merged.Ran && r.Ran
		merged.StartedAt = earliest(merged.StartedAt, r.StartedAt)
		merged.FinishedAt = latest(merged.FinishedAt, r.FinishedAt)
		if r.Passed {
			continue
		}
		if reason := strings.TrimSpace(r.Reason); reason != "" {
			reasons = append(reasons, reason)
		}
		if f := strings.TrimSpace(r.Feedback); f != "" {
			feedback = append(feedback, f)
		}
	}
	merged.Reason = strings.Join(reasons, "; ")
	merged.Feedback = strings.Join(feedback, "\n\n")
	merged.State = mergedState(merged.Tests, reports)

	if err := merged.Validate(); err != nil {
		return VerifyReport{}, fmt.Errorf("merge verify reports %q: %w", name, err)
	}
	return merged, nil
}

// reportNode is one report as a group node: its own tests below it, its own
// summary on it. The summary is carried rather than recomputed so a report that
// elided rows still contributes every one of them to the round's totals.
func reportNode(r VerifyReport) VerifyNode {
	summary := r.Summary
	return VerifyNode{
		Name:      r.Name,
		Framework: r.Kind,
		Message:   r.Reason,
		Duration:  r.Duration,
		Summary:   &summary,
		Children:  r.Tests,
	}
}

// mergedState is the tree's state, except that a host-stamped state (errored,
// cancelled) is the host's word about a runner that never reported and no node
// flag maps to it — so it survives the merge rather than being overwritten by
// whatever the queued leaves it left behind imply. errored outranks cancelled:
// a round that broke did not merely stop.
func mergedState(tests []VerifyNode, reports []VerifyReport) VerifyState {
	stamped := VerifyState("")
	for _, r := range reports {
		switch {
		case !r.State.HostStamped():
		case r.State == VerifyStateErrored:
			return VerifyStateErrored
		case stamped == "":
			stamped = r.State
		}
	}
	if stamped != "" {
		return stamped
	}
	return StateForReport(tests)
}

// mergedKind keeps the reports' kind when they share one; a mixed round is a
// round, not a member of any one verifier family.
func mergedKind(reports []VerifyReport) string {
	kind := reports[0].Kind
	for _, r := range reports[1:] {
		if r.Kind != kind {
			return VerifyKindRound
		}
	}
	if kind == "" {
		return VerifyKindRound
	}
	return kind
}

func sharedIteration(name string, reports []VerifyReport) (int, error) {
	iteration := reports[0].Iteration
	for _, r := range reports[1:] {
		if r.Iteration != iteration {
			return 0, fmt.Errorf("merge verify reports %q: %q judged iteration %d but %q judged iteration %d; a round's reports judge one turn",
				name, reports[0].Name, iteration, r.Name, r.Iteration)
		}
	}
	return iteration, nil
}

func earliest(into, from *time.Time) *time.Time {
	if from == nil || (into != nil && into.Before(*from)) {
		return into
	}
	return from
}

func latest(into, from *time.Time) *time.Time {
	if from == nil || (into != nil && into.After(*from)) {
		return into
	}
	return from
}
