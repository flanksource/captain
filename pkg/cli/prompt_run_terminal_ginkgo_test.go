package cli

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Prompt terminal", func() {
	schema := json.RawMessage(`{"type":"object"}`)
	workflow := &api.Workflow{AutoVerifyWithoutFixture: true}

	rendered := func(withSchema, withWorkflow bool) PromptRenderResult {
		var r PromptRenderResult
		r.Provider, r.Mode = "anthropic", "cli"
		if withSchema {
			r.Input.Prompt.SchemaJSON = schema
		}
		if withWorkflow {
			r.Input.Workflow = workflow
		}
		return r
	}

	DescribeTable("streamsToTerminal follows the path the run takes",
		func(withSchema, withWorkflow, noStream, want bool) {
			Expect(streamsToTerminal(rendered(withSchema, withWorkflow), noStream)).To(Equal(want))
		},
		Entry("a plain direct call streams", false, false, false, true),
		Entry("a direct call with a schema is buffered", true, false, false, false),
		Entry("the runner streams a schema prompt", true, true, false, true),
		Entry("the runner streams a workflow prompt", false, true, false, true),
		Entry("--no-stream buffers the runner", false, true, true, false),
		Entry("--no-stream buffers a direct call", false, false, true, false),
	)

	It("takes the logger output for a streaming run and returns it on Close", func() {
		before := logger.GetOutput()
		r := rendered(false, true)

		ctx, terminal, err := attachPromptTerminal(context.Background(), &r, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(terminal).NotTo(BeNil())
		Expect(promptEventRenderer(ctx)).To(BeIdenticalTo(terminal.renderer))
		Expect(r.Config.OnApproval).To(BeNil(), "a cli-mode run asks no approvals at the terminal")
		Expect(logger.GetOutput()).To(Equal(terminal.renderer.Writer()))

		Expect(terminal.Close()).To(Succeed())
		Expect(logger.GetOutput()).To(Equal(before))
	})

	It("leaves stderr alone for a run that neither streams nor asks approvals", func() {
		before := logger.GetOutput()
		r := rendered(false, false)

		ctx, terminal, err := attachPromptTerminal(context.Background(), &r, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(terminal).To(BeNil())
		Expect(promptEventRenderer(ctx)).To(BeNil())
		Expect(logger.GetOutput()).To(Equal(before))
	})
})
