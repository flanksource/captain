package runowner_test

import (
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/runowner"
	"github.com/google/uuid"
)

// exitedPID is the PID of a process that ran and was reaped, so the OS no
// longer knows it.
func exitedPID() int64 {
	GinkgoHelper()
	cmd := exec.Command("true")
	Expect(cmd.Start()).To(Succeed())
	pid := int64(cmd.Process.Pid)
	Expect(cmd.Wait()).To(Succeed())
	return pid
}

var _ = Describe("Judge", func() {
	const remoteHost = "build-runner-7"

	var now time.Time

	BeforeEach(func() { now = time.Now().UTC() })

	judge := func(run database.PromptRun) (bool, string) { return runowner.Judge(now)(run) }
	at := func(age time.Duration) *time.Time {
		stamp := now.Add(-age)
		return &stamp
	}
	remote := func(heartbeat *time.Time) *database.PromptRunOwner {
		return &database.PromptRunOwner{HostID: remoteHost, PID: 4242, StartedAt: now.Add(-time.Hour), Token: uuid.New(), HeartbeatAt: heartbeat}
	}

	It("finds a run driven by this process alive", func() {
		self, err := runowner.Local()
		Expect(err).NotTo(HaveOccurred())

		alive, reason := judge(database.PromptRun{Owner: &self})

		Expect(alive).To(BeTrue())
		Expect(reason).To(ContainSubstring("is running"))
	})

	It("finds a run whose local driver has exited orphaned", func() {
		pid := exitedPID()
		owner := &database.PromptRunOwner{HostID: database.LocalHostID(), PID: pid, StartedAt: now.Add(-time.Minute), Token: uuid.New()}

		alive, reason := judge(database.PromptRun{Owner: owner})

		Expect(alive).To(BeFalse())
		Expect(reason).To(Equal("driver " + owner.Describe() + " has exited"))
	})

	It("finds a run orphaned when its driver's PID now belongs to a later process", func() {
		self, err := runowner.Local()
		Expect(err).NotTo(HaveOccurred())
		recycled := self
		recycled.StartedAt = self.StartedAt.Add(-time.Hour)

		alive, reason := judge(database.PromptRun{Owner: &recycled})

		Expect(alive).To(BeFalse())
		Expect(reason).To(ContainSubstring("the pid now belongs to a process started"))
	})

	DescribeTable("an owner on another host, by its heartbeat",
		func(age time.Duration, wantAlive bool, wantReason string) {
			alive, reason := judge(database.PromptRun{Owner: remote(at(age))})

			Expect(alive).To(Equal(wantAlive))
			Expect(reason).To(Equal(wantReason))
		},
		Entry("fresh within the TTL", 20*time.Second, true, "driver build-runner-7 pid 4242 last reported 20s ago"),
		Entry("stale past the TTL", runowner.HeartbeatTTL+time.Second, false, "driver build-runner-7 pid 4242 last reported 1m31s ago"),
	)

	It("finds an owner on another host that never sent a heartbeat orphaned", func() {
		alive, reason := judge(database.PromptRun{Owner: remote(nil)})

		Expect(alive).To(BeFalse())
		Expect(reason).To(Equal("driver build-runner-7 pid 4242 never recorded a heartbeat"))
	})

	DescribeTable("a run admitted before ownership was recorded, by when it last changed",
		func(age time.Duration, wantAlive bool, wantReason string) {
			alive, reason := judge(database.PromptRun{UpdatedAt: now.Add(-age)})

			Expect(alive).To(Equal(wantAlive))
			Expect(reason).To(Equal(wantReason))
		},
		Entry("changed recently", 10*time.Second, true, "no driver claimed the run; it last changed 10s ago"),
		Entry("untouched for a day", 28*time.Hour, false, "no driver claimed the run; it last changed 28h0m0s ago"),
	)
})
