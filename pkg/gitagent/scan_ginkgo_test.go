package gitagent

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The ingest watcher upserts whatever ScanTasks returns, so the scanner is the
// component that decides what history looks like. Two properties matter most:
// it must survive a half-written task directory (dispatch is not atomic across
// files), and it must not call a task finished while the protocol would still
// let the agent retry.
var _ = Describe("task scanning", func() {
	var repo string

	BeforeEach(func() {
		repo = GinkgoT().TempDir()
	})

	writeState := func(task string, state TaskState) {
		state.Task = task
		Expect(SaveTaskState(repo, &state)).To(Succeed())
	}
	writeVerdict := func(task string, verdict TierVerdict) {
		verdict.Task = task
		Expect(SaveVerdict(repo, verdict)).To(Succeed())
	}

	It("returns nothing for a repository nothing was dispatched to", func() {
		snapshots, err := ScanTasks(repo)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshots).To(BeEmpty())
	})

	It("reads a task's state and every tier's verdict, ordered", func() {
		writeState("task-1", TaskState{Base: "main", DispatchCommit: "deadbeef", Attempts: 2})
		writeVerdict("task-1", TierVerdict{V: 1, Attempt: 2, Tier: "supervisor", Status: StatusAccepted})
		writeVerdict("task-1", TierVerdict{V: 1, Attempt: 1, Tier: "supervisor", Status: StatusRejected})

		snapshots, err := ScanTasks(repo)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshots).To(HaveLen(1))

		snapshot := snapshots[0]
		Expect(snapshot.Task).To(Equal("task-1"))
		Expect(snapshot.State).NotTo(BeNil())
		Expect(snapshot.State.Attempts).To(Equal(2))
		Expect(snapshot.Verdicts).To(HaveLen(2))
		Expect(snapshot.Verdicts[0].Attempt).To(Equal(1), "verdicts sort by attempt")
		Expect(snapshot.Verdicts[1].Attempt).To(Equal(2))
	})

	// Dispatch writes state.json and the verdicts directory at different
	// moments, so a scan can land between them. That must yield a partial
	// snapshot, not an error that aborts the whole ingest pass.
	It("tolerates a task directory with no state yet", func() {
		Expect(os.MkdirAll(filepath.Join(repo, "captain", "tasks", "task-1"), 0o755)).To(Succeed())

		snapshots, err := ScanTasks(repo)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshots).To(HaveLen(1))
		Expect(snapshots[0].State).To(BeNil())
		Expect(snapshots[0].Verdicts).To(BeEmpty())
	})

	It("ignores directories that are not task ids", func() {
		Expect(os.MkdirAll(filepath.Join(repo, "captain", "tasks", "../escape"), 0o755)).
			To(Or(Succeed(), HaveOccurred()))
		Expect(os.MkdirAll(filepath.Join(repo, "captain", "tasks", "not a task id"), 0o755)).To(Succeed())
		writeState("task-1", TaskState{Base: "main", DispatchCommit: "deadbeef"})

		snapshots, err := ScanTasks(repo)
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshots).To(HaveLen(1))
		Expect(snapshots[0].Task).To(Equal("task-1"))
	})

	Describe("deriving a terminal state", func() {
		It("treats an accepted supervisor verdict as the end", func() {
			writeState("task-1", TaskState{Base: "main", DispatchCommit: "c", Attempts: 1})
			writeVerdict("task-1", TierVerdict{V: 1, Attempt: 1, Tier: "supervisor", Status: StatusAccepted})

			snapshot, err := ScanTask(repo, "task-1")
			Expect(err).NotTo(HaveOccurred())
			status, done := snapshot.Concluded()
			Expect(done).To(BeTrue())
			Expect(status).To(Equal(StatusAccepted))
		})

		// §6.3: rejection is not termination. The agent may push again, so a
		// rejected attempt with budget left is still in flight.
		It("does not treat a rejection with attempts remaining as the end", func() {
			writeState("task-1", TaskState{
				Base: "main", DispatchCommit: "c", Attempts: 1,
				Policy: Policy{MaxAttempts: 3},
			})
			writeVerdict("task-1", TierVerdict{V: 1, Attempt: 1, Tier: "supervisor", Status: StatusRejected})

			snapshot, err := ScanTask(repo, "task-1")
			Expect(err).NotTo(HaveOccurred())
			_, done := snapshot.Concluded()
			Expect(done).To(BeFalse())
		})

		It("treats a rejection that exhausts the attempt budget as the end", func() {
			writeState("task-1", TaskState{
				Base: "main", DispatchCommit: "c", Attempts: 3,
				Policy: Policy{MaxAttempts: 3},
			})
			writeVerdict("task-1", TierVerdict{V: 1, Attempt: 3, Tier: "supervisor", Status: StatusRejected})

			snapshot, err := ScanTask(repo, "task-1")
			Expect(err).NotTo(HaveOccurred())
			status, done := snapshot.Concluded()
			Expect(done).To(BeTrue())
			Expect(status).To(Equal(StatusRejected))
		})

		// On disk a verdict is keyed by attempt alone (verdicts/<n>.json), so one
		// receiver holds at most one per attempt and a second write for the same
		// attempt replaces the first. The store still keys on (attempt, tier)
		// because the supervisor's mailbox and a sidecar's repo are separate
		// trees whose verdicts coexist once both are ingested.
		It("keeps only the last verdict written for an attempt", func() {
			writeState("task-1", TaskState{Base: "main", DispatchCommit: "c", Attempts: 1})
			writeVerdict("task-1", TierVerdict{V: 1, Attempt: 1, Tier: "sidecar", Status: StatusAccepted})
			writeVerdict("task-1", TierVerdict{V: 1, Attempt: 1, Tier: "supervisor", Status: StatusRejected})

			snapshot, err := ScanTask(repo, "task-1")
			Expect(err).NotTo(HaveOccurred())
			Expect(snapshot.Verdicts).To(HaveLen(1))
			Expect(snapshot.Verdicts[0].Tier).To(Equal("supervisor"))
		})

		// Pure logic, exercised directly: once both tiers' verdicts have been
		// ingested from their separate trees they sit side by side, and the
		// supervisor's is the one that concludes the task because it integrates.
		It("prefers the supervisor's decision over the sidecar's at equal attempt", func() {
			snapshot := TaskSnapshot{
				Task:  "task-1",
				State: &TaskState{Attempts: 1, Policy: Policy{MaxAttempts: 1}},
				Verdicts: []TierVerdict{
					{Attempt: 1, Tier: "sidecar", Status: StatusAccepted},
					{Attempt: 1, Tier: "supervisor", Status: StatusRejected},
				},
			}
			status, done := snapshot.Concluded()
			Expect(done).To(BeTrue())
			Expect(status).To(Equal(StatusRejected),
				"the sidecar accepting must not mask the supervisor rejecting")
		})

		It("reports no conclusion when no verdict has landed", func() {
			writeState("task-1", TaskState{Base: "main", DispatchCommit: "c"})
			snapshot, err := ScanTask(repo, "task-1")
			Expect(err).NotTo(HaveOccurred())
			_, done := snapshot.Concluded()
			Expect(done).To(BeFalse())
		})
	})

	It("surfaces the branch accepted work was integrated onto", func() {
		writeState("task-1", TaskState{Base: "main", DispatchCommit: "c", Attempts: 1})
		writeVerdict("task-1", TierVerdict{
			V: 1, Attempt: 1, Tier: "supervisor", Status: StatusAccepted,
			Findings: []Finding{{Hook: "integrate", Kind: "commit", Path: "captain/task-1"}},
		})

		snapshot, err := ScanTask(repo, "task-1")
		Expect(err).NotTo(HaveOccurred())
		Expect(snapshot.IntegratedBranch()).To(Equal("captain/task-1"))
	})

	Describe("VerdictAttempt", func() {
		It("accepts a positive attempt file and rejects anything else", func() {
			attempt, ok := VerdictAttempt("3.json")
			Expect(ok).To(BeTrue())
			Expect(attempt).To(Equal(3))

			for _, name := range []string{"0.json", "-1.json", "latest.json", "3", "3.txt", ".json"} {
				_, ok := VerdictAttempt(name)
				Expect(ok).To(BeFalse(), name)
			}
		})
	})
})

var _ = Describe("mailbox scanning", func() {
	It("returns nothing when no mailbox has been created", func() {
		mailboxes, err := ScanMailboxes(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		Expect(mailboxes).To(BeEmpty())
	})

	It("lists only entries in the opaque mailbox namespace", func() {
		root := GinkgoT().TempDir()
		valid := "mailboxes/" + "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90" + ".git"
		Expect(os.MkdirAll(filepath.Join(root, valid), 0o755)).To(Succeed())
		// A stray directory must not be reported as a mailbox: its tasks would
		// be ingested under a route the protocol never issued.
		Expect(os.MkdirAll(filepath.Join(root, "mailboxes", "scratch"), 0o755)).To(Succeed())

		mailboxes, err := ScanMailboxes(root)
		Expect(err).NotTo(HaveOccurred())
		Expect(mailboxes).To(HaveLen(1))
		Expect(mailboxes[0].Route).To(Equal(valid))
	})
})
