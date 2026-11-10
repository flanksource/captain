package middleware

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Schema repair strictness spellings", func() {
	DescribeTable("repairs a buffered response that violates the schema", func(policy string) {
		inner := &scriptedProvider{responses: []string{overCapJSON, validJSON}}
		provider, err := WithSchemaValidation()(inner)
		Expect(err).NotTo(HaveOccurred())
		resp, err := provider.Execute(context.Background(), capRequest(api.SchemaStrictness(policy)))
		Expect(err).NotTo(HaveOccurred())
		Expect(inner.calls).To(Equal(2))
		Expect(resp.Text).To(MatchJSON(validJSON))
	}, Entry("canonical", "repair"), Entry("compatibility alias", "retry"))

	DescribeTable("repairs a streamed result that violates the schema", func(policy string) {
		inner := &streamingProvider{results: []string{overCapJSON, validJSON}}
		provider, err := WithSchemaValidation()(inner)
		Expect(err).NotTo(HaveOccurred())
		events, err := provider.(ai.StreamingProvider).ExecuteStream(context.Background(), capRequest(api.SchemaStrictness(policy)))
		Expect(err).NotTo(HaveOccurred())
		var received []ai.Event
		for event := range events {
			received = append(received, event)
		}
		Expect(inner.requests).To(HaveLen(2))
		Expect(eventsOfKind(received, ai.EventError)).To(BeEmpty())
		results := eventsOfKind(received, ai.EventResult)
		Expect(results).To(HaveLen(1))
		Expect(results[0].StructuredData).To(MatchJSON(validJSON))
	}, Entry("canonical", "repair"), Entry("compatibility alias", "retry"))
})
