package agentbrowser

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("browser session listing", func() {
	newer := time.Now().Add(-5 * time.Minute)
	older := time.Now().Add(-2 * time.Hour)

	sessions := func() []BrowserSession {
		return []BrowserSession{
			{Session: "stale-one", State: StateStale, DaemonPID: 999001},
			{
				Session: "old-live", State: StateLive, DaemonPID: 9921, StartedAt: &older,
				CPUPercent: 0.4, MemoryRSSKB: 184320, Project: "clicky-ui",
				Link: BrowserLink{Source: BrowserLinkAncestor, AgentSource: "codex", AgentSessionID: "01f2a9c0"},
			},
			{
				Session: "new-live", State: StateLive, DaemonPID: 11473, StartedAt: &newer,
				CPUPercent: 3.1, MemoryRSSKB: 421888, Project: "captain",
				Link: BrowserLink{Source: BrowserLinkEnv, AgentSource: "claude", AgentSessionID: "8c630de6"},
			},
		}
	}

	Describe("filterBrowserSessions", func() {
		It("hides sessions whose daemon has exited unless --all is passed", func() {
			Expect(filterBrowserSessions(sessions(), BrowserListOptions{})).To(HaveLen(2))
			Expect(filterBrowserSessions(sessions(), BrowserListOptions{All: true})).To(HaveLen(3))
		})

		It("matches the query against the linked agent session, not just the browser name", func() {
			matched := filterBrowserSessions(sessions(), BrowserListOptions{All: true, Query: "8c630de6"})

			Expect(matched).To(HaveLen(1))
			Expect(matched[0].Session).To(Equal("new-live"))
		})

		It("matches on the daemon pid", func() {
			matched := filterBrowserSessions(sessions(), BrowserListOptions{All: true, Query: "9921"})

			Expect(matched).To(HaveLen(1))
			Expect(matched[0].Session).To(Equal("old-live"))
		})
	})

	Describe("sortBrowserSessions", func() {
		It("puts live sessions first, newest daemon first", func() {
			ordered := sessions()
			sortBrowserSessions(ordered)

			names := make([]string, len(ordered))
			for i, session := range ordered {
				names[i] = session.Session
			}
			Expect(names).To(Equal([]string{"new-live", "old-live", "stale-one"}))
		})
	})

	Describe("browserListResult", func() {
		It("totals the cost across every listed session", func() {
			result := browserListResult("/sockets", sessions())

			Expect(result.Total).To(Equal(3))
			Expect(result.Live).To(Equal(2))
			Expect(result.Stale).To(Equal(1))
			Expect(result.CPU).To(Equal("3.5%"))
			Expect(result.Memory).To(Equal("592MB"))
		})
	})

	Describe("humanRSS", func() {
		It("renders ps kilobytes at a readable scale", func() {
			Expect(FormatRSS(512)).To(Equal("512KB"))
			Expect(FormatRSS(421888)).To(Equal("412MB"))
			Expect(FormatRSS(2718608)).To(Equal("2.6GB"))
		})
	})
})
