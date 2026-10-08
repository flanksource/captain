package claudeagent

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Agent bridge protocol version", func() {
	It("is declared by the embedded bridge exactly as captain expects it", func() {
		Expect(protocolTS).To(ContainSubstring(fmt.Sprintf("export const PROTOCOL_VERSION = %d;", bridgeProtocolVersion)))
	})

	DescribeTable("refuses a bridge that does not speak the current protocol",
		func(mode string) {
			withFakeAgentProcessEnv(GinkgoT(), map[string]string{fakeServerEnv: "1", fakeModeEnv: mode})
			provider, err := New(ai.Config{Model: api.Model{Name: testModel}})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = provider.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			_, err = provider.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{User: "hello"}})

			Expect(err).To(MatchError(And(
				ContainSubstring(fmt.Sprintf("needs bridge protocol %d", bridgeProtocolVersion)),
				ContainSubstring("restart"),
			)))
		},
		Entry("an initialize reply with no protocolVersion", fakeModeUnversionedBridge),
		Entry("an initialize reply from an older bridge", fakeModeOldBridge),
	)
})

var _ = Describe("Claude Agent caller-tool approval scope", func() {
	It("resolves each call's context from the active turn", func() {
		provider := &Provider{model: testModel}
		options, err := provider.callerToolOptions(ai.Request{}, nil)
		Expect(err).NotTo(HaveOccurred())

		Expect(options.ContextForCall()).To(BeNil())

		turnCtx := context.WithValue(context.Background(), struct{ name string }{"turn"}, "active")
		provider.setActive(&turnState{ctx: turnCtx})
		Expect(options.ContextForCall()).To(BeIdenticalTo(turnCtx))
	})

	It("bounds caller-tool approval by the run's approvalTimeout", func() {
		provider := &Provider{model: testModel}

		options, err := provider.callerToolOptions(ai.Request{Permissions: api.Permissions{ApprovalTimeout: "90s"}}, nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(options.ApprovalTimeout).To(Equal(90 * time.Second))
	})

	It("fails on an approvalTimeout it cannot parse", func() {
		provider := &Provider{model: testModel}

		_, err := provider.callerToolOptions(ai.Request{Permissions: api.Permissions{ApprovalTimeout: "soon"}}, nil)

		Expect(err).To(MatchError(ContainSubstring(`invalid permissions approvalTimeout "soon"`)))
	})
})
