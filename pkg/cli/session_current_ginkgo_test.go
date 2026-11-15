package cli

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CurrentLiveSession", func() {
	const (
		shellPID  = 4100
		claudePID = 4000
		codexPID  = 3000
	)
	claude := SessionRecord{ID: "claude-live", Source: "claude", Live: &SessionLiveWire{PID: claudePID}}
	codex := SessionRecord{ID: "codex-live", Source: "codex", Live: &SessionLiveWire{PID: codexPID}}
	ended := SessionRecord{ID: "claude-ended", Source: "claude"}

	current := func(env map[string]string, ancestors []int) (*SessionRecord, error) {
		return currentLiveSession(context.Background(), currentLiveRuntime{
			getenv:    func(key string) string { return env[key] },
			ancestors: func(context.Context) ([]int, error) { return ancestors, nil },
			live: func(context.Context) ([]SessionRecord, error) {
				return []SessionRecord{codex, claude, ended}, nil
			},
		})
	}

	DescribeTable("resolves the session this process runs inside",
		func(env map[string]string, ancestors []int, expected *SessionRecord) {
			Expect(current(env, ancestors)).To(Equal(expected))
		},
		Entry("nearest live ancestor over an inherited outer marker",
			map[string]string{"CODEX_THREAD_ID": "codex-live", "CLAUDECODE": "1"}, []int{shellPID, claudePID, codexPID}, &claude),
		Entry("bare provider marker via the ancestor agent",
			map[string]string{"CLAUDECODE": "1"}, []int{shellPID, claudePID}, &claude),
		Entry("marker id when no ancestor is visible",
			map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-live"}, []int{shellPID}, &claude),
		Entry("no agent marker", map[string]string{}, []int{shellPID, claudePID}, nil),
	)

	DescribeTable("fails when the marked session is not live",
		func(env map[string]string, message string) {
			_, err := current(env, []int{shellPID})
			Expect(err).To(MatchError(message))
		},
		Entry("ended session id", map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-ended"},
			"CLAUDE_CODE_SESSION_ID=claude-ended names no live claude session"),
		Entry("bare marker without a live ancestor", map[string]string{"CLAUDECODE": "1"},
			"CLAUDECODE marks a claude session, but no ancestor of this process is a live session"),
	)
})
