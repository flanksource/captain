package gitagent_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
)

var _ = Describe("task ids", func() {
	It("accepts the documented shape", func() {
		Expect(gitagent.ValidateTaskID("01jb-refactor-store")).To(Succeed())
		Expect(gitagent.ValidateTaskID("a")).To(Succeed())
	})

	It("rejects everything outside ^[a-z0-9-]{1,64}$", func() {
		for _, bad := range []string{"", "UPPER", "has space", "dot.dot", "a/b", "..", "-" + string(make([]byte, 64))} {
			Expect(gitagent.ValidateTaskID(bad)).NotTo(Succeed(), "task id %q", bad)
		}
		long := ""
		for range 65 {
			long += "a"
		}
		Expect(gitagent.ValidateTaskID(long)).NotTo(Succeed())
	})
})

var _ = Describe("attempts", func() {
	It("parses positive decimals without leading zeros", func() {
		n, err := gitagent.ParseAttempt("1")
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(1))
		n, err = gitagent.ParseAttempt("42")
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(42))
	})

	It("rejects zero, leading zeros, negatives and junk", func() {
		for _, bad := range []string{"0", "01", "-1", "", "1x", "0x1", "1.0", "9999999"} {
			_, err := gitagent.ParseAttempt(bad)
			Expect(err).To(HaveOccurred(), "attempt %q", bad)
		}
	})
})

var _ = Describe("protocol refs", func() {
	It("round-trips all four kinds", func() {
		for _, kind := range []gitagent.RefKind{gitagent.RefDispatch, gitagent.RefControl, gitagent.RefResult, gitagent.RefVerdict} {
			ref, err := gitagent.TaskRef("my-task", kind, 3)
			Expect(err).NotTo(HaveOccurred())
			Expect(ref).To(Equal("refs/captain/tasks/my-task/" + string(kind) + "/3"))
			info, err := gitagent.ParseTaskRef(ref)
			Expect(err).NotTo(HaveOccurred())
			Expect(info).To(Equal(gitagent.RefInfo{Task: "my-task", Kind: kind, Attempt: 3}))
		}
	})

	It("rejects malformed refs", func() {
		for _, bad := range []string{
			"refs/heads/main",
			"refs/captain/tasks/my-task",
			"refs/captain/tasks/my-task/dispatch",
			"refs/captain/tasks/my-task/dispatch/0",
			"refs/captain/tasks/my-task/dispatch/01",
			"refs/captain/tasks/my-task/unknown/1",
			"refs/captain/tasks/My-Task/dispatch/1",
			"refs/captain/tasks/my-task/dispatch/1/extra",
		} {
			_, err := gitagent.ParseTaskRef(bad)
			Expect(err).To(HaveOccurred(), "ref %q", bad)
		}
	})

	It("builds the agent branch", func() {
		ref, err := gitagent.AgentBranch("my-task")
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(Equal("refs/heads/captain/my-task"))
		_, err = gitagent.AgentBranch("Bad Task")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("namespace containment (R8.3/H11)", func() {
	It("appends the separator before matching", func() {
		ns := gitagent.TaskNamespace("a")
		Expect(gitagent.NamespaceContains(ns, "refs/captain/tasks/a/result/1")).To(BeTrue())
		// agent "a" must not reach agent "ab"'s namespace
		Expect(gitagent.NamespaceContains(ns, "refs/captain/tasks/ab/result/1")).To(BeFalse())
		Expect(gitagent.NamespaceContains(ns, "refs/captain/tasks/a")).To(BeFalse())
	})

	It("rejects an empty namespace", func() {
		Expect(gitagent.NamespaceContains("", "refs/captain/tasks/a/result/1")).To(BeFalse())
	})
})
