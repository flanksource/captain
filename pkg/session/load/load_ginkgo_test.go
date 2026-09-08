package load_test

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/load"
)

func TestLoad(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Session Load Suite")
}

// stub is a contributor that claims one facet and, if it wins the claim, writes
// a marker message naming itself.
type stub struct {
	source load.Source
	facet  load.Facet
	err    error
}

func (s stub) Source() load.Source { return s.source }

func (s stub) Contribute(_ context.Context, aggregate *session.Session, prov *load.Provenance) error {
	if s.err != nil {
		return s.err
	}
	if !prov.Claim(s.facet, s.source) {
		return nil
	}
	aggregate.Messages = append(aggregate.Messages, session.Message{ID: string(s.source)})
	return nil
}

var _ = Describe("Load", func() {
	It("runs every contributor, not one chosen branch", func() {
		// The defect this package replaces: the prompt run's facts were dropped
		// whenever a transcript existed, because only one producer ever ran.
		result, failures := load.Load(context.Background(),
			stub{source: load.SourcePromptRun, facet: load.FacetPrompt},
			stub{source: load.SourceTranscript, facet: load.FacetMessages},
			stub{source: load.SourceRequests, facet: load.FacetApprovals},
		)

		Expect(failures).To(BeEmpty())
		Expect(messageIDs(result.Session)).To(ConsistOf("transcript", "prompt-run", "requests"))
	})

	It("gives a contested facet to the higher-precedence source regardless of argument order", func() {
		result, _ := load.Load(context.Background(),
			stub{source: load.SourcePromptRun, facet: load.FacetMessages},
			stub{source: load.SourceDatabase, facet: load.FacetMessages},
			stub{source: load.SourceTranscript, facet: load.FacetMessages},
		)

		Expect(result.Provenance.Of(load.FacetMessages)).To(Equal(load.SourceTranscript))
		Expect(messageIDs(result.Session)).To(Equal([]string{"transcript"}))
	})

	It("lets a lower source supply a facet the higher one left absent", func() {
		result, _ := load.Load(context.Background(),
			stub{source: load.SourceTranscript, facet: load.FacetMessages},
			stub{source: load.SourcePromptRun, facet: load.FacetPrompt},
		)

		Expect(result.Provenance.Of(load.FacetMessages)).To(Equal(load.SourceTranscript))
		Expect(result.Provenance.Of(load.FacetPrompt)).To(Equal(load.SourcePromptRun))
	})

	It("reports an unread facet rather than implying nothing happened", func() {
		result, _ := load.Load(context.Background(),
			stub{source: load.SourcePromptRun, facet: load.FacetMessages})

		Expect(result.Provenance.Of(load.FacetMessages)).To(Equal(load.SourcePromptRun))
		Expect(result.Provenance.Of(load.FacetUsage)).To(Equal(load.SourceNone))
		Expect(result.Provenance.Facets()).To(HaveKeyWithValue("usage", "none"))
	})

	It("keeps the other sources when one fails, and reports the failure", func() {
		// A transcript that will not parse must not hide the approval blocking
		// the run: the old code returned a hard error for the whole read.
		result, failures := load.Load(context.Background(),
			stub{source: load.SourceTranscript, facet: load.FacetMessages, err: fmt.Errorf("codex auto-review session")},
			stub{source: load.SourceRequests, facet: load.FacetApprovals},
		)

		Expect(failures).To(HaveLen(1))
		Expect(failures[0].Error()).To(ContainSubstring("transcript: codex auto-review session"))
		Expect(messageIDs(result.Session)).To(Equal([]string{"requests"}))
	})

	It("treats a source with nothing to say as ordinary", func() {
		_, failures := load.Load(context.Background(),
			stub{source: load.SourceTranscript, facet: load.FacetMessages, err: load.ErrNoFacts})

		Expect(failures).To(BeEmpty())
	})
})

func messageIDs(s *session.Session) []string {
	ids := make([]string, 0, len(s.Messages))
	for _, message := range s.Messages {
		ids = append(ids, message.ID)
	}
	return ids
}
