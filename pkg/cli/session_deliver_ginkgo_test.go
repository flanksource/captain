package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session/deliver"
	clickyprocess "github.com/flanksource/clicky/process"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
})
