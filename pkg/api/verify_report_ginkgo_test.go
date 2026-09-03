package api_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("VerifyReport", func() {
	leaf := func(name string, set func(*api.VerifyNode)) api.VerifyNode {
		n := api.VerifyNode{Name: name}
		set(&n)
		return n
	}
	passed := func(n *api.VerifyNode) { n.Passed = true }
	failed := func(n *api.VerifyNode) { n.Failed = true }

	Describe("wire shape", func() {
		It("marshals with clicky-ui's snake_case Test keys", func() {
			started := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)
			report := api.NewNodeReport(api.VerifyKindCmd, "verify:go test", api.VerifyNode{
				Name: "go test", TaskID: "t1", WorkDir: "/repo", TimedOut: true, Duration: time.Second,
				Context: &api.VerifyNodeContext{Command: "go test", ExitCode: 2, Cwd: "/repo", CELExpression: "exitCode == 0"},
			})
			report.StartedAt = &started

			raw, err := json.Marshal(report)
			Expect(err).NotTo(HaveOccurred())
			var doc map[string]any
			Expect(json.Unmarshal(raw, &doc)).To(Succeed())

			Expect(doc).To(HaveKey("started_at"))
			node := doc["tests"].([]any)[0].(map[string]any)
			Expect(node).To(HaveKeyWithValue("task_id", "t1"))
			Expect(node).To(HaveKeyWithValue("work_dir", "/repo"))
			Expect(node).To(HaveKeyWithValue("timed_out", true))
			context := node["context"].(map[string]any)
			Expect(context).To(HaveKeyWithValue("exit_code", 2.0))
			Expect(context).To(HaveKeyWithValue("cel_expression", "exitCode == 0"))
			Expect(report.State).To(Equal(api.VerifyStateTimedOut))
			Expect(report.Passed).To(BeFalse())
		})
	})

	Describe("SummarizeNodes", func() {
		It("counts nested leaves only and never a group", func() {
			tree := []api.VerifyNode{{
				Name: "suite",
				Children: []api.VerifyNode{
					leaf("a", passed),
					leaf("b", failed),
					leaf("c", func(n *api.VerifyNode) { n.Warned = true }),
					{Name: "nested", Children: []api.VerifyNode{
						leaf("d", func(n *api.VerifyNode) { n.Running = true }),
						leaf("e", func(n *api.VerifyNode) { n.Pending = true }),
					}},
				},
			}}
			Expect(api.SummarizeNodes(tree)).To(Equal(api.VerifySummary{Total: 5, Passed: 1, Failed: 1, Warned: 1, Running: 1, Pending: 1}))
		})

		// clicky-ui's countsFromLeaf gives a flagless leaf total 0: it is a
		// placeholder row, not a queued test, and counting it inflates every
		// denominator a progress bar divides by.
		It("does not count a leaf carrying no status flag at all", func() {
			Expect(api.SummarizeNodes([]api.VerifyNode{leaf("blank", func(*api.VerifyNode) {})})).
				To(Equal(api.VerifySummary{}))
		})

		// A timed-out leaf is its own bucket, exactly as countsFromLeaf splits it:
		// folding it into failed loses the only signal that says "this never
		// finished" rather than "this ran and disagreed".
		It("counts a timed-out leaf as timed out and not as failed", func() {
			timedOut := leaf("t", func(n *api.VerifyNode) { n.TimedOut, n.Failed = true, true })
			Expect(api.SummarizeNodes([]api.VerifyNode{timedOut})).
				To(Equal(api.VerifySummary{Total: 1, TimedOut: 1}))
		})

		It("marshals the timed-out counter under clicky-ui's `timedout` key", func() {
			raw, err := json.Marshal(api.VerifySummary{Total: 1, TimedOut: 1})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"timedout":1`))
		})

		// clicky-ui's sum() reads a node's own summary before it looks at children,
		// which is what lets a producer ship the counts of a suite whose child list
		// it elided. Recursing anyway reported that suite as empty.
		It("counts a group from its own summary rather than from its children", func() {
			group := api.VerifyNode{
				Name: "acceptance", Framework: api.VerifyKindFixture,
				Summary:  &api.VerifySummary{Total: 40, Passed: 39, Failed: 1},
				Children: []api.VerifyNode{leaf("the one row that was kept", failed)},
			}
			Expect(api.SummarizeNodes([]api.VerifyNode{group})).
				To(Equal(api.VerifySummary{Total: 40, Passed: 39, Failed: 1}))
		})

		It("validates a report whose group carries a summary and an elided child list", func() {
			report := api.VerifyReport{
				Kind: api.VerifyKindFixture, Name: "acceptance", Ran: true, State: api.VerifyStateFailed,
				Tests: []api.VerifyNode{{
					Name: "acceptance", Framework: api.VerifyKindFixture,
					Summary: &api.VerifySummary{Total: 40, Passed: 39, Failed: 1},
				}},
				Summary: api.VerifySummary{Total: 40, Passed: 39, Failed: 1},
			}
			Expect(report.Validate()).To(Succeed())
		})

		It("marshals a node summary under `summary` and omits it when absent", func() {
			raw, err := json.Marshal(api.VerifyNode{Name: "suite", Summary: &api.VerifySummary{Total: 2, Passed: 2}})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"summary":{"total":2,"passed":2`))

			raw, err = json.Marshal(api.VerifyNode{Name: "leaf", Passed: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).NotTo(ContainSubstring("summary"))
		})
	})

	Describe("StateForReport", func() {
		DescribeTable("derives the report's state from the whole tree",
			func(nodes []api.VerifyNode, want api.VerifyState) {
				Expect(api.StateForReport(nodes)).To(Equal(want))
			},
			Entry("no tests at all is queued", []api.VerifyNode{}, api.VerifyStateQueued),
			Entry("a failure outranks everything", []api.VerifyNode{
				leaf("a", passed), leaf("b", failed), leaf("c", func(n *api.VerifyNode) { n.TimedOut = true }),
			}, api.VerifyStateFailed),
			Entry("a timeout outranks a warning", []api.VerifyNode{
				leaf("a", func(n *api.VerifyNode) { n.TimedOut = true }), leaf("b", func(n *api.VerifyNode) { n.Warned = true }),
			}, api.VerifyStateTimedOut),
			Entry("a warning outranks a still-running leaf", []api.VerifyNode{
				leaf("a", func(n *api.VerifyNode) { n.Warned = true }), leaf("b", func(n *api.VerifyNode) { n.Running = true }),
			}, api.VerifyStateWarned),
			Entry("anything still running keeps the report running", []api.VerifyNode{
				leaf("a", passed), leaf("b", func(n *api.VerifyNode) { n.Running = true }),
			}, api.VerifyStateRunning),
			Entry("anything still pending keeps the report queued", []api.VerifyNode{
				leaf("a", passed), leaf("b", func(n *api.VerifyNode) { n.Pending = true }),
			}, api.VerifyStateQueued),
			Entry("all skipped is skipped", []api.VerifyNode{leaf("a", func(n *api.VerifyNode) { n.Skipped = true })}, api.VerifyStateSkipped),
			Entry("all passed is passed", []api.VerifyNode{leaf("a", passed), leaf("b", passed)}, api.VerifyStatePassed),
		)

		It("agrees with StateForNode for every single-leaf report", func() {
			for _, node := range []api.VerifyNode{
				leaf("p", passed), leaf("f", failed),
				leaf("t", func(n *api.VerifyNode) { n.TimedOut, n.Failed = true, true }),
				leaf("w", func(n *api.VerifyNode) { n.Warned = true }),
				leaf("s", func(n *api.VerifyNode) { n.Skipped = true }),
				leaf("r", func(n *api.VerifyNode) { n.Running = true }),
				leaf("blank", func(*api.VerifyNode) {}),
				// Contradictory flags are where two hand-ordered switches drift: the
				// leaf branch preferred skipped over running and skipped over
				// pending, so NewNodeReport stamped a state Validate then rejected.
				leaf("skipped+running", func(n *api.VerifyNode) { n.Skipped, n.Running = true, true }),
				leaf("pending+skipped", func(n *api.VerifyNode) { n.Pending, n.Skipped = true, true }),
				leaf("failed+passed", func(n *api.VerifyNode) { n.Failed, n.Passed = true, true }),
			} {
				Expect(api.StateForReport([]api.VerifyNode{node})).To(Equal(api.StateForNode(node)),
					"NewNodeReport stamps StateForNode, and Validate checks StateForReport: the two must not disagree")
				Expect(api.NewNodeReport(api.VerifyKindCmd, node.Name, node).Validate()).To(Succeed(),
					"a report NewNodeReport built must survive its own Validate")
			}
		})
	})

	Describe("Validate", func() {
		It("accepts a report built from a passing leaf", func() {
			Expect(api.NewNodeReport(api.VerifyKindCmd, "ok", leaf("ok", passed)).Validate()).To(Succeed())
		})

		It("rejects passed=true with a failed state", func() {
			report := api.NewNodeReport(api.VerifyKindCmd, "bad", leaf("bad", failed))
			report.Passed = true
			Expect(report.Validate()).To(MatchError(ContainSubstring("passed=true with state \"failed\"")))
		})

		It("rejects a summary that disagrees with the leaves", func() {
			report := api.NewNodeReport(api.VerifyKindCmd, "drift", leaf("drift", passed))
			report.Summary.Failed = 1
			Expect(report.Validate()).To(MatchError(ContainSubstring("summary")))
		})

		It("rejects a report that did not run but claims a verdict state", func() {
			report := api.VerifyReport{Kind: api.VerifyKindCmd, State: api.VerifyStatePassed}
			Expect(report.Validate()).To(MatchError(ContainSubstring("ran=false")))
		})

		It("requires a kind and a known state", func() {
			Expect(api.VerifyReport{State: api.VerifyStateQueued}.Validate()).To(MatchError(ContainSubstring("kind is required")))
			Expect(api.VerifyReport{Kind: api.VerifyKindCmd, State: "nope"}.Validate()).To(MatchError(ContainSubstring("invalid verify state")))
		})

		// A state that disagrees with the tree is how a red run reads as green
		// downstream: the webapp colours the badge from State while the panel
		// lists the failures, and a CEL predicate on the state passes.
		It("rejects a state that its own tests do not justify", func() {
			report := api.NewNodeReport(api.VerifyKindCmd, "drift", leaf("drift", failed))
			report.State = api.VerifyStateWarned
			Expect(report.Validate()).To(MatchError(ContainSubstring(`state "warned" but its tests are "failed"`)))
		})

		// errored and cancelled are the host's word, not the tree's: a runner
		// that could not schedule its nodes, or a run stopped mid-check, leaves
		// queued leaves behind that say nothing about why. No node flag maps to
		// either state, so they are accepted as stamped — and only as failures.
		DescribeTable("accepts a host-stamped terminal state over nodes that never ran",
			func(state api.VerifyState) {
				report := api.VerifyReport{
					Kind: api.VerifyKindFixture, Name: "acceptance", State: state, Reason: "runner exited",
					Tests:   []api.VerifyNode{leaf("go test", func(n *api.VerifyNode) { n.Pending = true })},
					Summary: api.VerifySummary{Total: 1, Pending: 1},
				}
				Expect(report.Validate()).To(Succeed())

				report.Ran = true
				Expect(report.Validate()).To(Succeed(), "a check that started and then was cancelled did run")

				report.Passed = true
				Expect(report.Validate()).To(MatchError(ContainSubstring("passed=true with state")))
			},
			Entry("errored", api.VerifyStateErrored),
			Entry("cancelled", api.VerifyStateCancelled),
		)

		// The report is also the live snapshot a ProgressVerifier publishes: it
		// has not finished, so Ran is false while the tree is already running.
		It("accepts an in-flight snapshot that is running before it has run", func() {
			snapshot := api.VerifyReport{
				Kind: api.VerifyKindCmd, Ran: false, State: api.VerifyStateRunning,
				Tests:   []api.VerifyNode{leaf("go test", func(n *api.VerifyNode) { n.Running = true })},
				Summary: api.VerifySummary{Total: 1, Running: 1},
			}
			Expect(snapshot.Validate()).To(Succeed())
		})
	})

	Describe("CELVars", func() {
		It("exposes the summary counters as numbers and never a nil checklist", func() {
			vars, err := api.NewNodeReport(api.VerifyKindCmd, "cmd", leaf("cmd", failed)).CELVars()
			Expect(err).NotTo(HaveOccurred())
			summary := vars["summary"].(map[string]any)
			Expect(summary["failed"]).To(BeNumerically("==", 1))
			Expect(vars["passed"]).To(BeFalse())
			Expect(vars["checklist"]).To(Equal([]any{}))
		})

		// A detail a verifier could not encode is a broken report, and a host
		// binding it into a CEL predicate deserves the error rather than a
		// panic unwinding through its evaluator.
		It("returns an error for a detail that cannot be marshalled", func() {
			report := api.NewNodeReport(api.VerifyKindCmd, "cmd", leaf("cmd", failed))
			report.Tests[0].Detail = json.RawMessage(`{"unterminated":`)
			_, err := report.CELVars()
			Expect(err).To(MatchError(ContainSubstring("cmd")))
		})
	})

	Describe("Iteration", func() {
		// 1-based, and always on the wire: `omitempty` erased iteration 0, which
		// meant "unstamped" and "the first turn" arrived as the same document.
		It("marshals iteration 1 rather than dropping it as a zero value", func() {
			report := api.NewNodeReport(api.VerifyKindCmd, "cmd", leaf("cmd", passed))
			raw, err := json.Marshal(report)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"iteration":0`))

			report.Iteration = 1
			raw, err = json.Marshal(report)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"iteration":1`))
		})
	})
})
