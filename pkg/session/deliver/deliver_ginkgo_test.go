package deliver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/provider/cmux"
	"github.com/flanksource/captain/pkg/session/process"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	claudeSessionID = "8c630de6-bdfc-41e6-8dd6-b6d999347dbe"
	codexThreadID   = "01f2a9c0-0000-7000-8000-000000000000"
	messagingToken  = "token-acme"
	threadText      = "Handle playground feedback thread acme-1"
)

var cmuxSurface = &process.Surface{WorkspaceID: "workspace:ws1", SurfaceID: "surface:sf1"}

func claudeEnvironment(socket string) map[string]string {
	return map[string]string{
		"CLAUDE_CODE_SESSION_ID": claudeSessionID,
		EnvMessagingSocket:       socket,
		EnvMessagingToken:        messagingToken,
	}
}

var _ = Describe("ChooseChannel", func() {
	DescribeTable("prefers the agent's native channel over typing into its pane",
		func(target Target, expected Channel) {
			Expect(ChooseChannel(target)).To(Equal(expected))
		},
		Entry("claude with a messaging socket, in cmux",
			Target{Source: "claude", SessionID: claudeSessionID, Environment: claudeEnvironment("/tmp/cc.sock"), Surface: cmuxSurface},
			ChannelClaudeSocket),
		Entry("codex in cmux without a verified inbox drain, queued by session id",
			Target{Source: "codex", SessionID: codexThreadID, Surface: cmuxSurface},
			ChannelCodexQueue),
		Entry("codex outside cmux, queued on its thread by session id alone",
			Target{Source: "codex", SessionID: codexThreadID},
			ChannelCodexQueue),
		Entry("claude without a messaging socket, in cmux",
			Target{Source: "claude", SessionID: claudeSessionID, Surface: cmuxSurface},
			ChannelCmuxMessage),
		Entry("claude with an unusable socket, in cmux",
			Target{Source: "claude", SessionID: claudeSessionID, Environment: map[string]string{EnvMessagingSocket: "/tmp/cc.sock"}, Surface: cmuxSurface},
			ChannelCmuxMessage),
	)

	DescribeTable("names what is missing when no channel reaches the session",
		func(target Target, reason string) {
			_, err := ChooseChannel(target)
			Expect(err).To(MatchError(ContainSubstring(reason)))
		},
		Entry("claude outside cmux without a socket",
			Target{Source: "claude", SessionID: claudeSessionID},
			EnvMessagingSocket),
		Entry("a socket without its token",
			Target{Source: "claude", SessionID: claudeSessionID, Environment: map[string]string{EnvMessagingSocket: "/tmp/cc.sock"}},
			EnvMessagingToken),
		Entry("codex outside cmux without a session id",
			Target{Source: "codex"},
			"no session id"),
	)
})

var _ = Describe("ClaudeMessagingEnvironment", func() {
	const claudePID = 86227
	owned := func(pid string) map[string]string {
		env := claudeEnvironment("/tmp/cc-socks-501/" + pid + ".sock")
		env[EnvClaudePID] = pid
		return env
	}
	spoolForwarder := map[string]string{}

	DescribeTable("takes the environment claude exported to a child it spawned",
		func(children []map[string]string, expected map[string]string) {
			Expect(ClaudeMessagingEnvironment(claudePID, children)).To(Equal(expected))
		},
		Entry("cmux's inbox-wait helper carries the socket",
			[]map[string]string{spoolForwarder, owned("86227")}, owned("86227")),
		Entry("children without a socket carry nothing",
			[]map[string]string{spoolForwarder}, nil),
		Entry("a child stamped with an ancestor claude's pid is not this claude's",
			[]map[string]string{owned("2445")}, nil),
		Entry("no children", nil, nil),
	)
})

var _ = Describe("Deliver", func() {
	ctx := context.Background()

	It("authenticates and injects one user message into claude's messaging socket", func() {
		socket := filepath.Join(shortTempDir(), "cc.sock")
		lines := listenLines(socket)

		result, err := Deliverer{}.Deliver(ctx, Target{Source: "claude", SessionID: claudeSessionID, Environment: claudeEnvironment(socket)}, threadText)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Channel: ChannelClaudeSocket, Status: StatusSent}))
		Expect(<-lines).To(Equal([]map[string]any{
			{"type": "auth", "token": messagingToken},
			{"type": "user", "message": map[string]any{"role": "user", "content": threadText}},
		}))
	})

	It("refuses a message longer than claude's line limit before connecting", func() {
		_, err := Deliverer{}.Deliver(ctx,
			Target{Source: "claude", SessionID: claudeSessionID, Environment: claudeEnvironment("/nonexistent/cc.sock")},
			strings.Repeat("x", claudeLineLimit))
		Expect(err).To(MatchError(ContainSubstring("line limit")))
	})

	It("fails naming the socket when claude is not listening", func() {
		socket := filepath.Join(shortTempDir(), "gone.sock")
		_, err := Deliverer{}.Deliver(ctx, Target{Source: "claude", SessionID: claudeSessionID, Environment: claudeEnvironment(socket)}, threadText)
		Expect(err).To(And(MatchError(ErrNoChannel), MatchError(ContainSubstring(socket))))
	})

	It("queues the message on the codex thread named by its session id", func() {
		const codexHome = "/codex/profile"
		var home string
		var args []string
		deliverer := Deliverer{Codex: func(_ context.Context, envHome string, argv ...string) error {
			home = envHome
			args = argv
			return nil
		}}

		result, err := deliverer.Deliver(ctx, Target{Source: "codex", SessionID: codexThreadID, Environment: map[string]string{EnvCodexHome: codexHome}, Surface: cmuxSurface}, threadText)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Channel: ChannelCodexQueue, Status: StatusQueued}))
		Expect(home).To(Equal(codexHome))
		Expect(args).To(Equal([]string{"queue", "--thread", codexThreadID, "--message=" + threadText}))
	})

	It("hands the message to cmux's agent inbox for the surface instead of typing into its prompt", func() {
		var calls [][]string
		client := cmux.NewClient("")
		client.Runner = func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
			calls = append(calls, args)
			return "", nil
		}

		result, err := Deliverer{Cmux: client}.Deliver(ctx, Target{Source: "claude", SessionID: claudeSessionID, Surface: cmuxSurface}, threadText)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Channel: ChannelCmuxMessage, Status: StatusQueued}))
		Expect(calls).To(Equal([][]string{
			{"agent", "message", cmuxSurface.SurfaceID, "--from", messageSender, "--", threadText},
		}))
	})

	It("queues on the cmux surface after the Claude socket refuses a connection", func() {
		var calls []string
		client := cmux.NewClient("")
		client.Runner = func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			return "", nil
		}
		deliverer := Deliverer{Cmux: client, Dial: func(context.Context, string) (net.Conn, error) {
			calls = append(calls, "dial")
			return nil, os.ErrNotExist
		}}

		result, err := deliverer.Deliver(ctx, Target{Source: "claude", SessionID: claudeSessionID, Environment: claudeEnvironment("/tmp/cc.sock"), Surface: cmuxSurface}, threadText)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(Result{Channel: ChannelCmuxMessage, Status: StatusQueued}))
		Expect(calls).To(Equal([]string{"dial", "agent message surface:sf1 --from captain -- " + threadText}))
	})

	It("preserves a partial socket write error without sending again", func() {
		conn, peer := net.Pipe()
		DeferCleanup(peer.Close)
		client := cmux.NewClient("")
		client.Runner = func(context.Context, string, string, time.Duration, ...string) (string, error) {
			Fail("cmux must not send a second copy")
			return "", nil
		}
		deliverer := Deliverer{Cmux: client, Dial: func(context.Context, string) (net.Conn, error) {
			return partialSocketWrite{Conn: conn}, nil
		}}

		_, err := deliverer.Deliver(ctx, Target{Source: "claude", SessionID: claudeSessionID, Environment: claudeEnvironment("/tmp/cc.sock"), Surface: cmuxSurface}, threadText)

		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
	})

	DescribeTable("after the Claude socket refuses a connection, permits a resume only when cmux never started",
		func(cmuxErr error, wantNoChannel bool) {
			client := cmux.NewClient("")
			client.Runner = func(context.Context, string, string, time.Duration, ...string) (string, error) {
				return "", cmuxErr
			}
			deliverer := Deliverer{Cmux: client, Dial: func(context.Context, string) (net.Conn, error) {
				return nil, os.ErrNotExist
			}}

			_, err := deliverer.Deliver(ctx, Target{Source: "claude", SessionID: claudeSessionID, Environment: claudeEnvironment("/tmp/cc.sock"), Surface: cmuxSurface}, threadText)

			Expect(err).To(MatchError(ContainSubstring(cmuxSurface.SurfaceID)))
			Expect(errors.Is(err, ErrNoChannel)).To(Equal(wantNoChannel))
		},
		Entry("cmux binary missing", fmt.Errorf("%w: exec: \"cmux\": executable file not found in $PATH", cmux.ErrNotStarted), true),
		Entry("cmux ran and failed", errors.New("cmux agent message failed (exit 1): socket closed"), false),
	)

	It("names the surface when cmux refuses the agent message", func() {
		client := cmux.NewClient("")
		client.Runner = func(_ context.Context, _, _ string, _ time.Duration, _ ...string) (string, error) {
			return "", errors.New("cmux agent message failed (exit 1): agent messages are off")
		}

		_, err := Deliverer{Cmux: client}.Deliver(ctx, Target{Source: "claude", SessionID: claudeSessionID, Surface: cmuxSurface}, threadText)

		Expect(err).To(MatchError(And(ContainSubstring(cmuxSurface.SurfaceID), ContainSubstring("agent messages are off"))))
	})
})

var _ = Describe("Codex queue rejection", func() {
	DescribeTable("permits fallback only when the queue rejected the write",
		func(code int, stderr string, want bool) {
			Expect(codexQueueRejected(code, stderr)).To(Equal(want))
		},
		Entry("CLI without queue support", 2, "error: unrecognized subcommand 'queue'\n\nUsage: codex [COMMAND]", true),
		Entry("daemon without queue support", 1, "Error: the local app-server daemon does not support thread/queue/add; update or restart the local app-server daemon: failed to queue session message", true),
		Entry("thread absent from the app server", 1, "Error: failed to queue session message: thread/queue/add failed: failed to read thread: invalid thread-store request: no rollout found for thread id "+codexThreadID+" (code -32603)", true),
		Entry("server disconnected during send", 1, "Error: failed to queue session message: transport closed", false),
	)
})

type partialSocketWrite struct{ net.Conn }

func (partialSocketWrite) Write([]byte) (int, error) { return 1, io.ErrUnexpectedEOF }

// shortTempDir keeps unix socket paths under the 104-byte sun_path limit that
// a nested GinkgoT().TempDir() on macOS exceeds.
func shortTempDir() string {
	dir, err := os.MkdirTemp("/tmp", "deliver")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(os.RemoveAll, dir)
	return dir
}

// listenLines accepts one connection on socket and reports the JSON lines it
// sent before closing its side.
func listenLines(socket string) <-chan []map[string]any {
	listener, err := net.Listen("unix", socket)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(listener.Close)
	out := make(chan []map[string]any, 1)
	go func() {
		defer GinkgoRecover()
		conn, err := listener.Accept()
		Expect(err).NotTo(HaveOccurred())
		defer conn.Close()
		var lines []map[string]any
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			var line map[string]any
			Expect(json.Unmarshal(scanner.Bytes(), &line)).To(Succeed())
			lines = append(lines, line)
		}
		out <- lines
	}()
	return out
}
