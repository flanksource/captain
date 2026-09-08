// Package load composes one session aggregate from every source that holds
// facts about it, instead of choosing a single producer per request.
//
// Before this package there were three mutually exclusive producers of the same
// response shape, selected by incidental database state: whether the session had
// stored messages, and whether it had a recorded transcript path. Which facts a
// caller received — approvals, token usage, the prompt run's own prompt and
// result — depended on which branch happened to run, and nothing in the response
// said which one did. A run suspended on a tool approval reported no approvals at
// all, because approvals were loaded in only one of the three.
package load

// Facet is one group of session facts that more than one source can supply, and
// therefore needs a declared winner.
type Facet string

const (
	FacetMessages  Facet = "messages"
	FacetTurns     Facet = "turns"
	FacetUsage     Facet = "usage"
	FacetApprovals Facet = "approvals"
	FacetPrompt    Facet = "prompt"
)

// Source names where a facet's value came from. It is reported to the caller so
// an empty session is distinguishable from an unread one: a session whose
// messages came from "prompt-run" is one whose transcript has not been ingested,
// not one where nothing happened.
type Source string

const (
	SourceNone       Source = "none"
	SourceTranscript Source = "transcript"
	SourceDatabase   Source = "database"
	SourcePromptRun  Source = "prompt-run"
	SourceOverview   Source = "overview"
	SourceRequests   Source = "requests"
)

// Precedence is the order contributors run in, highest-authority first. It is
// the single declaration of who wins a contested facet; Provenance enforces it
// by first-write-wins, so the slice order and the precedence are the same fact.
var Precedence = []Source{
	SourceOverview,
	SourceTranscript,
	SourceDatabase,
	SourcePromptRun,
	SourceRequests,
}

// Provenance records which source supplied each facet. The zero value is ready
// to use.
type Provenance struct {
	facets map[Facet]Source
}

// Claim asks whether source may write facet, and records it when it may.
//
// Contributors run in Precedence order and the first writer wins, so a lower
// source can add facts an earlier one left absent without ever overwriting it.
func (p *Provenance) Claim(facet Facet, source Source) bool {
	if p.facets == nil {
		p.facets = map[Facet]Source{}
	}
	if _, taken := p.facets[facet]; taken {
		return false
	}
	p.facets[facet] = source
	return true
}

// Of reports the source that supplied facet, or SourceNone when nothing did.
func (p *Provenance) Of(facet Facet) Source {
	if source, ok := p.facets[facet]; ok {
		return source
	}
	return SourceNone
}

// Facets is the recorded provenance keyed by facet name, for serialising onto a
// response. Facets no source supplied are reported as SourceNone rather than
// omitted, so a reader never has to guess whether a facet was absent or unasked.
func (p *Provenance) Facets() map[string]string {
	out := make(map[string]string, len(FacetsAll))
	for _, facet := range FacetsAll {
		out[string(facet)] = string(p.Of(facet))
	}
	return out
}

// FacetsAll is every facet Provenance reports on.
var FacetsAll = []Facet{FacetMessages, FacetTurns, FacetUsage, FacetApprovals, FacetPrompt}
