package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	muxclient "github.com/flanksource/captain/pkg/ai/provider/cmux"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/cmux"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/deliver"
	clickyprocess "github.com/flanksource/clicky/process"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("live delivery permits a requested model or permission mode only when the live process runs it",
	func(recordedModel string, recordedMode api.PermissionMode, message liveMessage, conflict string) {
		item := SessionGetItem{
			CaptainID: "session-acme", Summary: SessionRecord{Model: recordedModel},
			Detail: &session.Session{PermissionMode: recordedMode},
		}

		err := liveMessageMatches(item, message, deliver.ChannelClaudeSocket)

		if conflict == "" {
			Expect(err).NotTo(HaveOccurred())
			return
		}
		Expect(chatErrorStatus(err)).To(Equal(http.StatusConflict))
		Expect(err).To(MatchError(SatisfyAll(ContainSubstring(conflict), ContainSubstring(string(deliver.ChannelClaudeSocket)))))
	},
	Entry("nothing requested keeps the current model and posture", "claude-opus-5-5", api.PermissionAuto, liveMessage{}, ""),
	Entry("the recorded model and posture", "claude-opus-5-5", api.PermissionAuto,
		liveMessage{Model: "claude-opus-5-5", PermissionMode: api.PermissionAuto}, ""),
	Entry("another model", "claude-opus-5-5", api.PermissionAuto, liveMessage{Model: "claude-sonnet-5"}, `model "claude-opus-5-5"`),
	Entry("any model when none is recorded", "", api.PermissionAuto, liveMessage{Model: "claude-sonnet-5"}, "an unrecorded model"),
	Entry("another posture", "claude-opus-5-5", api.PermissionAuto, liveMessage{PermissionMode: api.PermissionPlan}, `permission mode "auto"`),
	Entry("any posture when none is recorded", "claude-opus-5-5", api.PermissionMode(""),
		liveMessage{PermissionMode: api.PermissionDefault}, "an unrecorded permission mode"),
)

var _ = Describe("SessionHandler deliver", Ordered, func() {
	// Above every OS's pid limit, so PopulateEnvironments finds them gone and
	// keeps the environments the stubbed snapshot carries.
	const (
		livePID      = 9_900_680
		inboxWaitPID = 9_900_690
		mcpServerPID = 9_900_691
		zombiePID    = 9_900_692
	)
	var (
		db       *database.DB
		live     *database.Session
		ended    *database.Session
		server   *httptest.Server
		socket   string
		envByPID map[int]map[string]string
		// withZombie adds an exited-but-unreaped child to the claude process.
		withZombie bool
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_deliver"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		setCaptainDBForTest(db)
		discoverSessionProcesses = func() ([]agentProcess, error) { return nil, nil }
		readProcessEnvironment = func(_ context.Context, pid int, _ clickyprocess.EnvironmentOptions) (map[string]string, error) {
			env, ok := envByPID[pid]
			if !ok {
				return nil, os.ErrNotExist
			}
			return env, nil
		}
		discoverProcesses = func(context.Context, clickyprocess.SnapshotOptions) (*clickyprocess.Snapshot, error) {
			processes := []clickyprocess.Process{{PID: livePID, PPID: 1, Environment: envByPID[livePID]}}
			for _, child := range []int{inboxWaitPID, mcpServerPID} {
				processes = append(processes, clickyprocess.Process{PID: child, PPID: livePID, Environment: envByPID[child]})
			}
			if withZombie {
				processes = append(processes, clickyprocess.Process{PID: zombiePID, PPID: livePID, Status: "zombie"})
			}
			return clickyprocess.NewSnapshot(processes), nil
		}
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			discoverSessionProcesses = discoverAgentProcesses
			readProcessEnvironment = clickyprocess.Environment
			discoverProcesses = clickyprocess.Discover
			Expect(db.Close()).To(Succeed())
		})

		live, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())
		ended, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())
		now := time.Now().UTC()
		Expect(db.UpsertSessionProcess(ctx, database.SessionProcessInput{
			SessionID: live.ID, HostID: captainHostID(), BootID: "boot", PID: livePID,
			ProcessStartedAt: now.Add(-time.Minute), SampledAt: now, Status: "sleeping", Source: "claude",
		})).To(Succeed())

		dir, err := os.MkdirTemp("/tmp", "deliver")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(os.RemoveAll, dir)
		socket = filepath.Join(dir, "cc.sock")

		mux := http.NewServeMux()
		mux.Handle("/api/captain/sessions/", http.StripPrefix("/api/captain/sessions", SessionHandler()))
		server = httptest.NewServer(mux)
		DeferCleanup(server.Close)
	})

	BeforeEach(func() {
		withZombie = false
		// Claude exports its messaging socket only to what it spawns, so the
		// claude process's own environment carries none of it.
		envByPID = map[int]map[string]string{
			livePID:      {},
			mcpServerPID: {},
			inboxWaitPID: {
				"CLAUDE_CODE_SESSION_ID":   live.ProviderSessionID,
				deliver.EnvClaudePID:       strconv.Itoa(livePID),
				deliver.EnvMessagingSocket: socket,
				deliver.EnvMessagingToken:  "token-acme",
			},
		}
	})

	deliverTo := func(ctx context.Context, id, body string) (int, string) {
		GinkgoHelper()
		response := postSessionMessage(ctx, server.URL+"/api/captain/sessions/"+id+"/deliver", body)
		defer response.Body.Close()
		text, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		return response.StatusCode, string(text)
	}

	// listenOnSocket accepts one connection on the session's messaging socket
	// and sends the lines written to it.
	listenOnSocket := func() <-chan []string {
		GinkgoHelper()
		listener, err := net.Listen("unix", socket)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(listener.Close)
		received := make(chan []string, 1)
		go func() {
			defer GinkgoRecover()
			conn, err := listener.Accept()
			Expect(err).NotTo(HaveOccurred())
			defer conn.Close()
			var lines []string
			for scanner := bufio.NewScanner(conn); scanner.Scan(); {
				lines = append(lines, scanner.Text())
			}
			received <- lines
		}()
		return received
	}

	It("delivers past a child that exited but was not reaped", func(ctx SpecContext) {
		withZombie = true
		received := listenOnSocket()

		status, body := deliverTo(ctx, live.ProviderSessionID, `{"text":"Handle thread acme-2"}`)

		Expect(status).To(Equal(http.StatusAccepted), body)
		Eventually(received).Should(Receive(ContainElement(ContainSubstring("Handle thread acme-2"))))
	})

	It("writes the message into the live claude session's messaging socket", func(ctx SpecContext) {
		received := listenOnSocket()

		status, body := deliverTo(ctx, live.ProviderSessionID, `{"text":"Handle thread acme-1"}`)

		Expect(status).To(Equal(http.StatusAccepted), body)
		var result deliver.Result
		Expect(json.Unmarshal([]byte(body), &result)).To(Succeed())
		Expect(result).To(Equal(deliver.Result{Channel: deliver.ChannelClaudeSocket, Status: deliver.StatusSent}))
		Eventually(received).Should(Receive(Equal([]string{
			`{"token":"token-acme","type":"auth"}`,
			`{"message":{"role":"user","content":"Handle thread acme-1"},"type":"user"}`,
		})))
	})

	It("continues the live session over its socket through the message route", func(ctx SpecContext) {
		received := listenOnSocket()
		response := postSessionMessage(ctx, server.URL+"/api/captain/sessions/"+live.ProviderSessionID+"/message",
			`{"text":"Continue on the current process","messageId":"follow-up"}`)
		defer response.Body.Close()

		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusAccepted), string(body))
		var result map[string]any
		Expect(json.Unmarshal(body, &result)).To(Succeed())
		Expect(result).To(HaveKeyWithValue("channel", "claude-socket"))
		Expect(result).To(HaveKeyWithValue("messageId", "follow-up"))
		Expect(result).NotTo(HaveKey("runId"))
		Eventually(received).Should(Receive(ContainElement(ContainSubstring("Continue on the current process"))))
	})

	It("rejects an unknown worktree recovery before delivering to a live session", func(ctx SpecContext) {
		previousDeliverer := sessionDeliverer
		DeferCleanup(func() { sessionDeliverer = previousDeliverer })
		sessionDeliverer = deliver.Deliverer{Dial: func(context.Context, string) (net.Conn, error) {
			Fail("an invalid request must not reach the live session")
			return nil, os.ErrNotExist
		}}
		response := postSessionMessage(ctx, server.URL+"/api/captain/sessions/"+live.ProviderSessionID+"/message",
			`{"text":"Continue on the current process","worktree":"rebase"}`)
		defer response.Body.Close()

		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest), string(body))
		Expect(string(body)).To(ContainSubstring(`unknown worktree recovery "rebase"`))
	})

	It("refuses a permission mode the live session cannot be confirmed to run under, before sending", func(ctx SpecContext) {
		previousDeliverer := sessionDeliverer
		DeferCleanup(func() { sessionDeliverer = previousDeliverer })
		sessionDeliverer = deliver.Deliverer{Dial: func(context.Context, string) (net.Conn, error) {
			Fail("a refused permission mode must not reach the live session")
			return nil, os.ErrNotExist
		}}
		response := postSessionMessage(ctx, server.URL+"/api/captain/sessions/"+live.ProviderSessionID+"/message",
			`{"text":"Continue in plan mode","permissionMode":"plan"}`)
		defer response.Body.Close()

		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusConflict), string(body))
		Expect(string(body)).To(SatisfyAll(ContainSubstring("unrecorded permission mode"), ContainSubstring(`"plan"`)))
	})

	It("continues the original terminal over cmux when the socket is gone", func(ctx SpecContext) {
		older, newer := time.Now().Add(-time.Hour), time.Now()
		previousDiscovery, previousDeliverer := discoverSessionProcesses, sessionDeliverer
		DeferCleanup(func() {
			discoverSessionProcesses, sessionDeliverer = previousDiscovery, previousDeliverer
		})
		discoverSessionProcesses = func() ([]agentProcess, error) {
			return []agentProcess{
				{PID: livePID + 1, Command: "claude --resume " + live.ProviderSessionID, Source: "claude", StartedAt: &newer},
				{PID: livePID, Command: "claude --session-id " + live.ProviderSessionID, Source: "claude", StartedAt: &older, Surface: &CmuxSurface{SurfaceID: "surface-original"}},
			}, nil
		}
		var calls [][]string
		client := muxclient.NewClient("")
		client.Runner = func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
			calls = append(calls, args)
			return "", nil
		}
		sessionDeliverer = deliver.Deliverer{Cmux: client}
		response := postSessionMessage(ctx, server.URL+"/api/captain/sessions/"+live.ID.String()+"/message", `{"text":"Continue in the terminal"}`)
		defer response.Body.Close()

		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusAccepted), string(body))
		Expect(string(body)).To(ContainSubstring(`"channel":"cmux-message"`))
		Expect(calls).To(Equal([][]string{{"agent", "message", "surface-original", "--from", "captain", "--", "Continue in the terminal"}}))
	})

	It("checks resume prerequisites after finding no live process", func(ctx SpecContext) {
		response := postSessionMessage(ctx, server.URL+"/api/captain/sessions/"+ended.ProviderSessionID+"/message", `{"text":"Resume later"}`)
		defer response.Body.Close()

		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusUnprocessableEntity))
		Expect(string(body)).To(ContainSubstring("session has no working directory"))
	})

	DescribeTable("continues an external Codex thread through the message route",
		func(ctx SpecContext, inCmux bool, queueErr error, status int, channel deliver.Channel, errorText string) {
			thread, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
				ProviderSessionID: uuid.NewString(), Source: "codex", Provider: "openai", HostID: captainHostID(),
			})
			Expect(err).NotTo(HaveOccurred())
			const pid = livePID + 2
			envByPID[pid] = map[string]string{"CODEX_THREAD_ID": thread.ProviderSessionID}
			process := agentProcess{PID: pid, Source: "codex", Environment: envByPID[pid]}
			if inCmux {
				process.Surface = &CmuxSurface{SurfaceID: "surface-codex"}
			}
			previousDiscovery, previousDeliverer := discoverSessionProcesses, sessionDeliverer
			DeferCleanup(func() {
				discoverSessionProcesses, sessionDeliverer = previousDiscovery, previousDeliverer
			})
			discoverSessionProcesses = func() ([]agentProcess, error) { return []agentProcess{process}, nil }
			var cmuxCalls [][]string
			client := muxclient.NewClient("")
			client.Runner = func(_ context.Context, _, _ string, _ time.Duration, args ...string) (string, error) {
				cmuxCalls = append(cmuxCalls, args)
				return "", nil
			}
			var queueCalls [][]string
			sessionDeliverer = deliver.Deliverer{Cmux: client, Codex: func(_ context.Context, _ string, args ...string) error {
				queueCalls = append(queueCalls, args)
				return queueErr
			}}
			const message = "Continue this Codex thread"
			response := postSessionMessage(ctx, server.URL+"/api/captain/sessions/"+thread.ProviderSessionID+"/message",
				`{"text":"`+message+`","messageId":"codex-follow-up"}`)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.StatusCode).To(Equal(status), string(body))
			Expect(queueCalls).To(Equal([][]string{{"queue", "--thread", thread.ProviderSessionID, "--message=" + message}}))
			if channel == deliver.ChannelCmuxMessage {
				Expect(cmuxCalls).To(Equal([][]string{{"agent", "message", "surface-codex", "--from", "captain", "--", message}}))
			} else {
				Expect(cmuxCalls).To(BeEmpty())
			}
			if errorText != "" {
				Expect(string(body)).To(ContainSubstring(errorText))
				return
			}
			var result map[string]any
			Expect(json.Unmarshal(body, &result)).To(Succeed())
			Expect(result).To(And(HaveKeyWithValue("channel", string(channel)), HaveKeyWithValue("status", "queued"), HaveKeyWithValue("messageId", "codex-follow-up"), Not(HaveKey("runId"))))
		},
		Entry("native queue first", true, nil, http.StatusAccepted, deliver.ChannelCodexQueue, ""),
		Entry("cmux when the queue is unavailable", true, exec.ErrNotFound, http.StatusAccepted, deliver.ChannelCmuxMessage, ""),
		Entry("resume after both channels are unavailable", false, exec.ErrNotFound, http.StatusUnprocessableEntity, deliver.Channel(""), "session has no working directory"),
		Entry("report an uncertain queue result without retrying", true, io.ErrUnexpectedEOF, http.StatusBadGateway, deliver.Channel(""), io.ErrUnexpectedEOF.Error()),
	)

	It("checks resume prerequisites after both live channels are unavailable", func(ctx SpecContext) {
		envByPID[inboxWaitPID] = map[string]string{}
		response := postSessionMessage(ctx, server.URL+"/api/captain/sessions/"+live.ProviderSessionID+"/message", `{"text":"Continue here"}`)
		defer response.Body.Close()

		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusUnprocessableEntity))
		Expect(string(body)).To(ContainSubstring("session has no working directory"))
	})

	It("refuses a session with no live process rather than resuming it", func(ctx SpecContext) {
		status, body := deliverTo(ctx, ended.ProviderSessionID, `{"text":"hello"}`)
		Expect(status).To(Equal(http.StatusConflict))
		Expect(body).To(ContainSubstring("no live process"))
	})

	It("refuses a PID that now belongs to another session", func(ctx SpecContext) {
		envByPID[inboxWaitPID]["CLAUDE_CODE_SESSION_ID"] = ended.ProviderSessionID
		status, body := deliverTo(ctx, live.ProviderSessionID, `{"text":"hello"}`)
		Expect(status).To(Equal(http.StatusConflict))
		Expect(body).To(ContainSubstring("now runs session " + ended.ProviderSessionID))
	})

	It("ignores the messaging socket a nested claude inherited from its parent", func(ctx SpecContext) {
		envByPID[livePID] = map[string]string{
			"CLAUDE_CODE_SESSION_ID":   ended.ProviderSessionID,
			deliver.EnvClaudePID:       "13579",
			deliver.EnvMessagingSocket: socket,
			deliver.EnvMessagingToken:  "token-parent",
		}
		envByPID[inboxWaitPID] = map[string]string{}
		status, body := deliverTo(ctx, live.ProviderSessionID, `{"text":"hello"}`)
		Expect(status).To(Equal(http.StatusUnprocessableEntity), body)
		Expect(body).To(ContainSubstring(deliver.EnvMessagingSocket))
	})

	It("answers 422 naming the missing channel for a live session outside cmux without a socket", func(ctx SpecContext) {
		envByPID[inboxWaitPID] = map[string]string{}
		status, body := deliverTo(ctx, live.ProviderSessionID, `{"text":"hello"}`)
		Expect(status).To(Equal(http.StatusUnprocessableEntity))
		Expect(body).To(ContainSubstring(deliver.EnvMessagingSocket))
	})

	It("requires message text", func(ctx SpecContext) {
		status, body := deliverTo(ctx, live.ProviderSessionID, `{"text":"  "}`)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(body).To(ContainSubstring("message text is required"))
	})

	DescribeTable("falls back to the recorded pane after an unreadable environment only while the process runs",
		func(ctx SpecContext, source string, state string) {
			const pid = livePID + 3
			started := time.Now().Add(-time.Minute)
			running := state == "running"
			previousDiscovery, previousSnapshot := discoverSessionProcesses, discoverProcesses
			DeferCleanup(func() {
				discoverSessionProcesses, discoverProcesses = previousDiscovery, previousSnapshot
			})
			discoverSessionProcesses = func() ([]agentProcess, error) {
				return []agentProcess{{
					PID: pid, Source: source, StartedAt: &started, Command: source + " --session-id " + live.ProviderSessionID,
					Environment: map[string]string{"CLAUDE_CODE_SESSION_ID": live.ProviderSessionID},
					Surface:     &CmuxSurface{SurfaceID: "surface-recorded"},
				}}, nil
			}
			discoverProcesses = func(context.Context, clickyprocess.SnapshotOptions) (*clickyprocess.Snapshot, error) {
				switch state {
				case "exited":
					return clickyprocess.NewSnapshot(nil), nil
				case "reused":
					// A later process holds the PID, with no messaging children.
					reused := started.Add(time.Second)
					return clickyprocess.NewSnapshot([]clickyprocess.Process{{PID: pid, PPID: 1, StartedAt: &reused}}), nil
				}
				// The child's environment is unreadable, as when permission is denied.
				return clickyprocess.NewSnapshot([]clickyprocess.Process{
					{PID: pid, PPID: 1, StartedAt: &started},
					{PID: pid + 1, PPID: pid, EnvironmentError: "permission denied"},
				}), nil
			}
			item, err := resolveSessionItem(ctx, live.ProviderSessionID)
			Expect(err).NotTo(HaveOccurred())
			item.Summary.Source = source

			target, err := liveDeliveryTarget(ctx, item)

			if running {
				Expect(err).NotTo(HaveOccurred())
				Expect(target.Surface).To(Equal(&CmuxSurface{SurfaceID: "surface-recorded"}))
				return
			}
			var chatErr chatHTTPError
			Expect(errors.As(err, &chatErr)).To(BeTrue(), "%v", err)
			Expect(chatErr.status).To(Equal(http.StatusConflict))
		},
		Entry("claude still running", "claude", "running"),
		Entry("claude exited", "claude", "exited"),
		Entry("claude's PID reused by a later process", "claude", "reused"),
		Entry("codex still running", "codex", "running"),
		Entry("codex exited", "codex", "exited"),
		Entry("codex's PID reused by a later process", "codex", "reused"),
	)

	It("targets the current workspace when a live terminal has moved", func(ctx SpecContext) {
		previousDiscovery, previousSurfaces := discoverSessionProcesses, discoverCmuxSurfaces
		DeferCleanup(func() {
			discoverSessionProcesses, discoverCmuxSurfaces = previousDiscovery, previousSurfaces
		})
		discoverSessionProcesses = func() ([]agentProcess, error) {
			return []agentProcess{{PID: livePID, Surface: &CmuxSurface{
				SurfaceID: "surface-moved", WorkspaceID: "workspace-original",
			}}}, nil
		}
		discoverCmuxSurfaces = func() (map[string]cmux.Surface, error) {
			return map[string]cmux.Surface{"surface-moved": {
				ID: "surface-moved", Ref: "surface:42", WorkspaceID: "workspace-current", Workspace: "Current",
			}}, nil
		}
		envByPID[inboxWaitPID] = map[string]string{}

		item, err := resolveSessionItem(ctx, live.ProviderSessionID)
		Expect(err).NotTo(HaveOccurred())
		target, err := liveDeliveryTarget(ctx, item)

		Expect(err).NotTo(HaveOccurred())
		Expect(target.Surface).To(Equal(&CmuxSurface{
			SurfaceID: "surface-moved", SurfaceRef: "surface:42", WorkspaceID: "workspace-current", Workspace: "Current",
		}))
		Expect(deliver.ChooseChannel(target)).To(Equal(deliver.ChannelCmuxMessage))
	})
})
