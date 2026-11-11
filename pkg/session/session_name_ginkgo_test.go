package session

import (
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Claude session name", func() {
	const (
		aiTitle = "Investigate session listing titles"
		renamed = "captain-background"
		prompt  = "Fix the session name detection in the session listing"
	)

	build := func(transcript string) *Session {
		entries, err := claude.ReadHistory(strings.NewReader(transcript))
		Expect(err).NotTo(HaveOccurred())
		return buildSession(claude.ParsedSession{
			SessionID: "provider-session",
			Transcripts: []claude.ParsedTranscript{{
				Path: "/work/provider-session.jsonl", Entries: entries, ToolUses: claude.ExtractToolUsesWithTokens(entries),
			}},
		})
	}

	ginkgo.It("names the session after the latest /rename and lets it outrank the AI title", func() {
		recovered := build(`{"type":"custom-title","customTitle":"first-name","sessionId":"provider-session"}
{"type":"user","uuid":"u1","sessionId":"provider-session","message":{"role":"user","content":"` + prompt + `"}}
{"type":"ai-title","aiTitle":"` + aiTitle + `","sessionId":"provider-session"}
{"type":"custom-title","customTitle":"` + renamed + `","sessionId":"provider-session"}
{"type":"agent-name","agentName":"` + renamed + `","sessionId":"provider-session"}`)

		Expect([]string{recovered.Name, recovered.Title, recovered.InitialPrompt}).To(Equal([]string{renamed, renamed, prompt}))
		Expect(recovered.Events).To(BeEmpty())
	})

	ginkgo.It("leaves the name empty and keeps the AI title when the session was never renamed", func() {
		recovered := build(`{"type":"user","uuid":"u1","sessionId":"provider-session","message":{"role":"user","content":"` + prompt + `"}}
{"type":"ai-title","aiTitle":"` + aiTitle + `","sessionId":"provider-session"}`)

		Expect([]string{recovered.Name, recovered.Title}).To(Equal([]string{"", aiTitle}))
	})
})
