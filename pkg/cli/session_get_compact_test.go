package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/session"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs join the package's existing suite (see session_get_multi_test.go);
// Ginkgo permits only one RunSpecs per package.

// transcriptFixture builds a session detail with the given number of messages
// and events so paging can be observed on both collections.
func transcriptFixture(messages, events int) *session.Session {
	s := &session.Session{ID: "ae95d3f5-9303-45ae-8c93-a83e13c79811", Source: "claude"}
	for i := 0; i < messages; i++ {
		s.Messages = append(s.Messages, session.Message{
			ID:    fmt.Sprintf("m%d", i),
			Role:  "user",
			Parts: []session.Part{{Type: session.PartText, Text: fmt.Sprintf("message %d", i)}},
		})
	}
	for i := 0; i < events; i++ {
		s.Events = append(s.Events, session.Event{
			Type: "task_started", Scope: "session", UUID: fmt.Sprintf("e%d", i),
		})
	}
	return s
}

var _ = Describe("session get transcript paging", func() {
	It("bounds events alongside messages under --tail", func() {
		detail := transcriptFixture(50, 40)

		pageSessionTranscript(detail, SessionGetOptions{Tail: 5})

		Expect(detail.Messages).To(HaveLen(5))
		Expect(detail.Events).To(HaveLen(5),
			"events were previously unbounded, so --tail 5 still dumped every state row")
		Expect(detail.Messages[0].ID).To(Equal("m45"))
		Expect(detail.Events[0].UUID).To(Equal("e35"))
	})

	It("bounds events alongside messages under --offset/--limit", func() {
		detail := transcriptFixture(50, 40)

		pageSessionTranscript(detail, SessionGetOptions{Offset: 10, Limit: 5})

		Expect(detail.Messages).To(HaveLen(5))
		Expect(detail.Events).To(HaveLen(5))
		Expect(detail.Messages[0].ID).To(Equal("m10"))
		Expect(detail.Events[0].UUID).To(Equal("e10"))
	})

	It("returns everything when the limit is explicitly cleared", func() {
		detail := transcriptFixture(50, 40)

		pageSessionTranscript(detail, SessionGetOptions{Limit: 0})

		Expect(detail.Messages).To(HaveLen(50))
		Expect(detail.Events).To(HaveLen(40))
	})

	It("leaves a collection untouched when it is shorter than the window", func() {
		detail := transcriptFixture(3, 2)

		pageSessionTranscript(detail, SessionGetOptions{Tail: 10})

		Expect(detail.Messages).To(HaveLen(3))
		Expect(detail.Events).To(HaveLen(2))
	})
})

var _ = Describe("session get transcript filtering", func() {
	It("keeps only matching tool parts and their result rows", func() {
		const (
			readCallID = "read-call"
			bashCallID = "bash-call"
		)
		detail := &session.Session{
			Messages: []session.Message{
				{
					ID: "assistant-tools", Role: "assistant",
					Parts: []session.Part{
						{Type: session.PartTool, ToolName: "Read", ToolCallID: readCallID, Input: []byte(`{"file_path":"main.go"}`)},
						{Type: session.PartTool, ToolName: "Bash", ToolCallID: bashCallID, Input: []byte(`{"command":"go test ./pkg/cli"}`)},
					},
				},
				{
					ID: "read-result", Role: "tool",
					Parts: []session.Part{{Type: session.PartTool, ToolCallID: readCallID, Output: []byte(`"read output"`)}},
				},
				{
					ID: "bash-result", Role: "tool",
					Parts: []session.Part{{Type: session.PartTool, ToolCallID: bashCallID, Output: []byte(`"test output"`)}},
				},
			},
			Events: []session.Event{{Type: "task_started", UUID: "event-1"}},
		}

		Expect(filterSessionTranscript(detail, SessionGetOptions{Tools: []string{"B*"}})).To(Succeed())

		Expect(detail.Messages).To(HaveLen(2))
		Expect(detail.Messages[0].Parts).To(Equal([]session.Part{{
			Type: session.PartTool, ToolName: "Bash", ToolCallID: bashCallID, Input: []byte(`{"command":"go test ./pkg/cli"}`),
		}}))
		Expect(detail.Messages[1].ID).To(Equal("bash-result"))
		Expect(detail.Events).To(BeEmpty())
		Expect(detail.Window).To(Equal(&session.TranscriptWindow{Messages: 3, Events: 1, ToolCalls: 2}))
	})

	It("uses history Bash categories before applying the transcript window", func() {
		detail := &session.Session{Messages: []session.Message{
			{
				ID: "explore", Role: "assistant",
				Parts: []session.Part{{
					Type: session.PartTool, ToolName: "Bash", ToolCallID: "explore-call",
					Input: []byte(`{"command":"rg -n captain pkg"}`),
				}},
			},
			{
				ID: "test", Role: "assistant",
				Parts: []session.Part{{
					Type: session.PartTool, ToolName: "Bash", ToolCallID: "test-call",
					Input: []byte(`{"command":"go test ./pkg/cli"}`),
				}},
			},
			{
				ID: "chat", Role: "assistant",
				Parts: []session.Part{{Type: session.PartText, Text: "Focused tests passed."}},
			},
		}}
		opts := SessionGetOptions{Categories: []string{"test"}, Tail: 1}

		Expect(filterSessionTranscript(detail, opts)).To(Succeed())
		pageSessionTranscript(detail, opts)

		Expect(detail.Messages).To(HaveLen(1))
		Expect(detail.Messages[0].ID).To(Equal("test"))
		Expect(detail.Window).To(Equal(&session.TranscriptWindow{Messages: 3, ToolCalls: 2}))
	})
})

var _ = Describe("session get header", func() {
	It("does not repeat detail fields in the Captain header", func() {
		item := SessionGetItem{
			CaptainID:         "d81f885d-3d60-47c0-8122-a8124f2fbdd1",
			ProviderSessionID: "ae95d3f5-9303-45ae-8c93-a83e13c79811",
			Host:              "MacBook-Pro.local",
			DetailAvailable:   true,
			Summary: SessionRecord{
				ID: "ae95d3f5-9303-45ae-8c93-a83e13c79811", Source: "claude",
				Project: "captain", CWD: "/repo/captain", Messages: 4,
			},
			Detail: &session.Session{
				ID: "ae95d3f5-9303-45ae-8c93-a83e13c79811", Source: "claude",
				Project: "captain", CWD: "/repo/captain",
			},
		}

		rendered := item.Pretty().String()

		Expect(rendered).To(ContainSubstring("d81f885d-3d60-47c0-8122-a8124f2fbdd1"))
		Expect(rendered).To(ContainSubstring("MacBook-Pro.local"))
		Expect(strings.Count(rendered, "/repo/captain")).To(Equal(1),
			"CWD is a Summary row; the header must not repeat it")
		Expect(rendered).NotTo(ContainSubstring("Provider session:"),
			"the provider session id is already the detail's ID row")
	})

	It("keeps identifying metadata when no transcript is available", func() {
		item := SessionGetItem{
			CaptainID:         "7ca78c55-e280-50ff-a19a-9f355a6fc55e",
			ProviderSessionID: "ad4c854e-cde6-4b99-99f3-667bf74112e3",
			Host:              "local",
			Summary:           SessionRecord{Source: "gavel", Project: "xero-cli", CWD: "/repo/xero"},
		}

		rendered := item.Pretty().String()

		Expect(rendered).To(ContainSubstring("Transcript: unavailable"))
		Expect(rendered).To(ContainSubstring("ad4c854e-cde6-4b99-99f3-667bf74112e3"))
		Expect(rendered).To(ContainSubstring("xero-cli"))
		Expect(rendered).To(ContainSubstring("/repo/xero"))
	})

	It("reports how many transcript rows the window hid", func() {
		detail := transcriptFixture(200, 0)
		pageSessionTranscript(detail, SessionGetOptions{Tail: 5})
		item := SessionGetItem{
			CaptainID: "d81f885d-3d60-47c0-8122-a8124f2fbdd1",
			Summary:   SessionRecord{Messages: 200},
			Detail:    detail,
		}

		rendered := item.Pretty().String()

		Expect(rendered).To(ContainSubstring("195 of 200 messages hidden"))
		Expect(rendered).To(ContainSubstring("--limit 0"))
	})

	It("stays silent when the whole transcript is shown", func() {
		detail := transcriptFixture(4, 0)
		item := SessionGetItem{
			CaptainID: "d81f885d-3d60-47c0-8122-a8124f2fbdd1",
			Summary:   SessionRecord{Messages: 4},
			Detail:    detail,
		}

		Expect(item.Pretty().String()).NotTo(ContainSubstring("hidden"))
	})

	It("does not claim --limit 0 restores rows excluded by filters", func() {
		item := SessionGetItem{
			CaptainID:          "d81f885d-3d60-47c0-8122-a8124f2fbdd1",
			Summary:            SessionRecord{Messages: 4},
			Detail:             transcriptFixture(1, 0),
			transcriptFiltered: true,
		}

		rendered := item.Pretty().String()

		Expect(rendered).To(ContainSubstring("showing 1 of 4 messages after transcript filters and windowing"))
		Expect(rendered).NotTo(ContainSubstring("--limit 0"))
	})
})
