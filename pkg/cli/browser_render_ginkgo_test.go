package cli

import (
	clickyapi "github.com/flanksource/clicky/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("browser session rendering", func() {
	It("qualifies a namespaced session and leaves an unlinked agent blank", func() {
		row := BrowserSession{Session: "default", Namespace: "designpg", State: browserStateLive}.Row()

		Expect(row["session"]).To(Equal("designpg/default"))
		Expect(row["agent"]).To(Equal(clickyapi.Text{}))
	})

	It("shows the owning agent source and short session id", func() {
		row := BrowserSession{
			Session: "captain-fb7c9f910548", State: browserStateLive,
			Link: BrowserLink{AgentSource: "claude", AgentSessionID: "8c630de6-bdfc-41e6-8dd6-b6d999347dbe"},
		}.Row()

		Expect(row["agent"]).To(Equal(psSourceText("claude").Space().Append("8c630de6", "text-muted")))
	})
})
