package monitor

import (
	"os"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

// statementLog captures the SQL of every statement a DB issues while recording,
// so a spec can assert what one steady-state monitor tick costs.
type statementLog struct {
	mu        sync.Mutex
	recording bool
	sql       []string
}

func recordStatements(db *database.DB) *statementLog {
	GinkgoHelper()
	log := &statementLog{}
	capture := func(tx *gorm.DB) {
		log.mu.Lock()
		defer log.mu.Unlock()
		if log.recording {
			log.sql = append(log.sql, tx.Statement.SQL.String())
		}
	}
	callbacks := db.Gorm().Callback()
	Expect(callbacks.Create().After("gorm:create").Register("test:capture_create", capture)).To(Succeed())
	Expect(callbacks.Query().After("gorm:query").Register("test:capture_query", capture)).To(Succeed())
	Expect(callbacks.Update().After("gorm:update").Register("test:capture_update", capture)).To(Succeed())
	Expect(callbacks.Delete().After("gorm:delete").Register("test:capture_delete", capture)).To(Succeed())
	Expect(callbacks.Raw().After("gorm:raw").Register("test:capture_raw", capture)).To(Succeed())
	Expect(callbacks.Row().After("gorm:row").Register("test:capture_row", capture)).To(Succeed())
	return log
}

func (l *statementLog) start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recording, l.sql = true, nil
}

func (l *statementLog) stop() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recording = false
	return l.sql
}

var _ = Describe("monitor steady-state writes", Ordered, func() {
	const hostID = "write-path-test"
	var (
		db         *database.DB
		statements *statementLog
		started    = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_monitor_write_path"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		statements = recordStatements(db)
	})

	// The heartbeat interval is an hour so every repeat poll below falls inside it.
	newMonitor := func(processes *[]Process) *Monitor {
		GinkgoHelper()
		m, err := New(Config{
			DB: db, HostID: hostID, IdleProcessInterval: time.Hour,
			DiscoverProcesses: func() ([]Process, error) { return *processes, nil },
		})
		Expect(err).NotTo(HaveOccurred())
		return m
	}

	It("binds a process to its environment session id without a process-row lookup", func(ctx SpecContext) {
		const envSessionID = "0199e2aa-0000-7000-8000-00000000e001"
		processes := []Process{{
			Source: "claude", PID: 4101, Status: "sleeping", StartedAt: &started,
			CWD: "/work/env", Command: "claude", ProviderSessionID: envSessionID,
		}}
		m := newMonitor(&processes)

		statements.start()
		Expect(m.pollProcesses(ctx, nil)).To(Succeed())
		Expect(statements.stop()).NotTo(ContainElement(ContainSubstring(`FROM "captain_session_processes"`)))

		bound, err := db.FindProcessSessionID(ctx, hostID, bootID(), 4101, started)
		Expect(err).NotTo(HaveOccurred())
		session, err := db.GetSession(ctx, bound)
		Expect(err).NotTo(HaveOccurred())
		Expect(session.ProviderSessionID).To(Equal(envSessionID))
	})

	It("issues no statement for an unchanged sample, and writes a changed one", func(ctx SpecContext) {
		processes := []Process{{
			Source: "claude", PID: 4102, Status: "sleeping", StartedAt: &started, CPUPercent: 1.5,
			CWD: "/work/argv", Command: "claude --session-id 0199e2aa-0000-7000-8000-00000000a001",
		}}
		m := newMonitor(&processes)
		Expect(m.pollProcesses(ctx, nil)).To(Succeed())

		processes[0].CPUPercent = 2.5 // metrics alone ride the heartbeat, not every poll
		statements.start()
		Expect(m.pollProcesses(ctx, nil)).To(Succeed())
		Expect(statements.stop()).To(BeEmpty())

		processes[0].Status = "active"
		statements.start()
		Expect(m.pollProcesses(ctx, nil)).To(Succeed())
		Expect(statements.stop()).To(ContainElement(ContainSubstring(`INSERT INTO "captain_session_processes"`)))
		var status string
		Expect(db.Gorm().Raw(`SELECT status FROM captain_session_processes WHERE pid = ? AND ended_at IS NULL`, 4102).
			Scan(&status).Error).To(Succeed())
		Expect(status).To(Equal("active"))
	})

	It("sweeps vanished processes on a tenure's first poll and when a PID vanishes, not on a steady set", func(ctx SpecContext) {
		const sweep = "host_id = $3 AND ended_at IS NULL"
		processes := []Process{
			{Source: "claude", PID: 4201, Status: "sleeping", StartedAt: &started, CWD: "/work/sweep",
				Command: "claude --session-id 0199e2aa-0000-7000-8000-00000000b001"},
			{Source: "claude", PID: 4202, Status: "sleeping", StartedAt: &started, CWD: "/work/sweep",
				Command: "claude --session-id 0199e2aa-0000-7000-8000-00000000b002"},
		}
		m := newMonitor(&processes)
		statements.start()
		Expect(m.pollProcesses(ctx, nil)).To(Succeed())
		Expect(statements.stop()).To(ContainElement(ContainSubstring(sweep)), "the first poll must sweep")

		statements.start()
		Expect(m.pollProcesses(ctx, nil)).To(Succeed())
		Expect(statements.stop()).NotTo(ContainElement(ContainSubstring(sweep)), "an unchanged live set needs no sweep")

		processes = processes[:1]
		statements.start()
		Expect(m.pollProcesses(ctx, nil)).To(Succeed())
		Expect(statements.stop()).To(ContainElement(ContainSubstring(sweep)), "a vanished PID must be swept")
		var open int
		Expect(db.Gorm().Raw(`SELECT count(*) FROM captain_session_processes WHERE pid = ? AND ended_at IS NULL`, 4202).
			Scan(&open).Error).To(Succeed())
		Expect(open).To(BeZero())
	})

	It("reads a live session's transcript path once, not on every poll", func(ctx SpecContext) {
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		const providerID = "0199e2aa-0000-7000-8000-00000000d001"
		_, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "claude", HostID: hostID,
			Path: "/home/dev/.claude/projects/-work-watch/" + providerID + ".jsonl",
		})
		Expect(err).NotTo(HaveOccurred())
		processes := []Process{{
			Source: "claude", PID: 4301, Status: "sleeping", StartedAt: &started,
			CWD: "/work/watch", Command: "claude --resume " + providerID,
		}}
		m := newMonitor(&processes)
		watcher, err := newTranscriptWatcher(m, newIngestor(m))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(watcher.close)
		Expect(m.pollProcesses(ctx, watcher)).To(Succeed())

		statements.start()
		Expect(m.pollProcesses(ctx, watcher)).To(Succeed())
		Expect(statements.stop()).NotTo(ContainElement(ContainSubstring(`FROM "captain_sessions"`)))
	})

	It("answers a repeated status-line refresh from the session identity cache", func(ctx SpecContext) {
		var processes []Process
		m := newMonitor(&processes)
		refresh := func(cost float64) {
			m.recordClaudeStatusLineCost(ctx, HookEvent{
				Provider: "claude", Event: ClaudeEventStatusLine, SessionID: "0199e2aa-0000-7000-8000-00000000c001",
				ClaudeCLICostUSD: &cost, ReceivedAt: started,
			}, "/home/dev/.claude/projects/-work/0199e2aa-0000-7000-8000-00000000c001.jsonl")
		}
		refresh(0.25)

		statements.start()
		refresh(0.5)
		Expect(statements.stop()).To(ConsistOf(ContainSubstring("claude_cli_cost_usd")))
	})
})

var _ = Describe("monitor writer lock connection", func() {
	It("labels only the holder's connection and never returns a labeled one to the pool", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_monitor_lock_label"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		labeled := func() int {
			var count int
			Expect(db.Gorm().Raw(`SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND application_name LIKE ?`, monitorLockAppPrefix+"%").
				Scan(&count).Error).To(Succeed())
			return count
		}
		holder, err := New(Config{DB: db, HostID: "lock-label-test"})
		Expect(err).NotTo(HaveOccurred())
		standby, err := New(Config{DB: db, HostID: "lock-label-test"})
		Expect(err).NotTo(HaveOccurred())

		conn, _, err := holder.tryAcquireWriterLock(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(conn).NotTo(BeNil())
		standbyConn, holderPID, err := standby.tryAcquireWriterLock(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(standbyConn).To(BeNil())
		Expect(holderPID).To(Equal(os.Getpid()), "the holder's own connection must keep its label")
		Eventually(labeled).Should(Equal(1), "the standby's probe connection must not stay labeled")

		Expect(releaseWriterLock(conn)).To(Succeed())
		Eventually(labeled).Should(BeZero(), "a released lock connection must not stay labeled")
		next, _, err := standby.tryAcquireWriterLock(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(next).NotTo(BeNil(), "releasing must drop the advisory lock")
		Expect(releaseWriterLock(next)).To(Succeed())
	})
})
