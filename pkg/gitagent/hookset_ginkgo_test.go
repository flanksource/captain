package gitagent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/gitagent"
)

// identityWrap satisfies the mandatory confinement seam for tests without a
// real sandbox runtime.
func identityWrap(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
	return cmd, args, env, nil
}

// judgeStub is an ai.Provider that returns a fixed judge verdict with no live
// model call.
type judgeStub struct {
	verdict string
	judged  int
}

func (p *judgeStub) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.judged++
	if req.Prompt.Schema != nil {
		// A real provider unmarshals the model's JSON into the structured
		// output target; the stub does the same with its canned verdict.
		if err := json.Unmarshal([]byte(p.verdict), req.Prompt.Schema); err != nil {
			return nil, err
		}
	}
	return &ai.Response{Text: p.verdict}, nil
}
func (p *judgeStub) GetModel() string        { return "stub" }
func (p *judgeStub) GetRuntime() api.Runtime { return api.RuntimeOf(api.Anthropic, api.ModeAPI) }

var _ = Describe("materialization", func() {
	ctx := context.Background()

	It("materializes the full tree into an absolute destination and counts it (R1.3)", func() {
		f := newAdmitFixture(ctx)
		dst := filepath.Join(GinkgoT().TempDir(), "tree")
		count, err := gitagent.Materialize(ctx, f.super, os.Environ(), f.snap.Commit, dst)
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(2)) // src/main.go + src/dirty.go
		Expect(os.ReadFile(filepath.Join(dst, "src", "dirty.go"))).To(Equal([]byte("package main // dirty\n")))
	})

	It("ignores unrelated files already present in the destination", func() {
		f := newAdmitFixture(ctx)
		dst := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dst, "stale.txt"), []byte("stale"), 0o644)).To(Succeed())
		count, err := gitagent.Materialize(ctx, f.super, os.Environ(), f.snap.Commit, dst)
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(2))
	})

	It("detects an empty or short materialization instead of passing (H18)", func() {
		Expect(gitagent.AssertMaterialized(0, 3)).To(MatchError(ContainSubstring("H18")))
		Expect(gitagent.AssertMaterialized(2, 3)).To(MatchError(ContainSubstring("H18")))
		Expect(gitagent.AssertMaterialized(3, 0)).To(MatchError(ContainSubstring("H18")))
		Expect(gitagent.AssertMaterialized(3, 3)).To(Succeed())
	})

	It("refuses a tree containing a .git path component (H9)", func() {
		Expect(gitagent.RejectDotGitComponents([]string{"a/b.txt", ".git/config"})).To(MatchError(ContainSubstring("H9")))
		Expect(gitagent.RejectDotGitComponents([]string{"a/.GIT/hook"})).To(MatchError(ContainSubstring("H9")))
		Expect(gitagent.RejectDotGitComponents([]string{"a/gitty/.gitignore"})).To(Succeed())
	})
})

var _ = Describe("hook sets", func() {
	ctx := context.Background()

	ws := func() gitagent.HookWorkspace {
		dir := GinkgoT().TempDir()
		writeFileT(dir, "main.go", "package main\n")
		return gitagent.HookWorkspace{Dir: dir, Changed: []string{"main.go"}}
	}

	It("accepts an empty workflow and a passing chain", func() {
		v := gitagent.RunHookSet(ctx, ws(), gitagent.HookSetOptions{Task: "t-1", Attempt: 1, Tier: "sidecar"})
		Expect(v.Status).To(Equal(gitagent.StatusAccepted))

		v = gitagent.RunHookSet(ctx, ws(), gitagent.HookSetOptions{
			Task: "t-1", Attempt: 1, Tier: "sidecar",
			Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}},
			Wrap:     identityWrap,
		})
		Expect(v.Status).To(Equal(gitagent.StatusAccepted))
		Expect(v.Rejects()).To(BeFalse())
	})

	It("stops at the first failing hook and carries its feedback (R5.1)", func() {
		w := ws()
		marker := filepath.Join(w.Dir, "second-ran")
		v := gitagent.RunHookSet(ctx, w, gitagent.HookSetOptions{
			Task: "t-1", Attempt: 1, Tier: "sidecar",
			Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{
				"echo boom-detail && false",
				"touch " + marker,
			}}},
			Wrap: identityWrap,
		})
		Expect(v.Status).To(Equal(gitagent.StatusRejected))
		Expect(v.Findings).To(HaveLen(1))
		Expect(v.Findings[0].Feedback).To(ContainSubstring("boom-detail"))
		Expect(marker).NotTo(BeAnExistingFile(), "the chain must stop at the first failure")
	})

	It("refuses to run exec hooks without a confinement wrap (R5.2)", func() {
		v := gitagent.RunHookSet(ctx, ws(), gitagent.HookSetOptions{
			Task: "t-1", Attempt: 1, Tier: "sidecar",
			Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}},
		})
		Expect(v.Status).To(Equal(gitagent.StatusError))
		Expect(v.Rejects()).To(BeTrue(), "an indeterminate verdict rejects (R7.5)")
		Expect(v.Findings[0].Message).To(ContainSubstring("R5.2"))
	})

	It("kills a hook that overruns its timeout and reports error status", func() {
		v := gitagent.RunHookSet(ctx, ws(), gitagent.HookSetOptions{
			Task: "t-1", Attempt: 1, Tier: "sidecar",
			Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"sleep 30"}}},
			Wrap:     identityWrap,
			Timeout:  200 * time.Millisecond,
		})
		Expect(v.Status).To(Equal(gitagent.StatusRejected))
		Expect(v.Findings[0].Message).To(ContainSubstring("timed out"))
	})

	It("runs prompt hooks through a mock provider with no live model call", func() {
		promptPath := filepath.Join(GinkgoT().TempDir(), "review.prompt")
		Expect(os.WriteFile(promptPath, []byte("{{role \"user\"}}\nJudge {{cwd}}."), 0o644)).To(Succeed())

		reject := &judgeStub{verdict: `{"ok":false,"reason":"style","feedback":"rename Baz"}`}
		v := gitagent.RunHookSet(ctx, ws(), gitagent.HookSetOptions{
			Task: "t-1", Attempt: 1, Tier: "supervisor",
			Workflow: &api.Workflow{Verify: &api.Verify{Prompts: []string{promptPath}}},
			Judge:    reject,
		})
		Expect(v.Status).To(Equal(gitagent.StatusRejected))
		Expect(reject.judged).To(Equal(1))
		Expect(v.Findings[0].Kind).To(Equal("prompt"))
		Expect(v.Findings[0].Feedback).To(ContainSubstring("rename Baz"))

		accept := &judgeStub{verdict: `{"ok":true,"reason":"fine","feedback":""}`}
		v = gitagent.RunHookSet(ctx, ws(), gitagent.HookSetOptions{
			Task: "t-1", Attempt: 1, Tier: "supervisor",
			Workflow: &api.Workflow{Verify: &api.Verify{Prompts: []string{promptPath}}},
			Judge:    accept,
		})
		Expect(v.Status).To(Equal(gitagent.StatusAccepted))
	})

	It("errors on prompts with no judge, and bounds recursion depth (R5.4)", func() {
		promptPath := filepath.Join(GinkgoT().TempDir(), "review.prompt")
		Expect(os.WriteFile(promptPath, []byte("{{role \"user\"}}\nJudge {{cwd}}."), 0o644)).To(Succeed())
		wf := &api.Workflow{Verify: &api.Verify{Prompts: []string{promptPath}}}

		v := gitagent.RunHookSet(ctx, ws(), gitagent.HookSetOptions{Task: "t-1", Attempt: 1, Workflow: wf})
		Expect(v.Status).To(Equal(gitagent.StatusError))
		Expect(v.Findings[0].Message).To(ContainSubstring("no provider"))

		v = gitagent.RunHookSet(ctx, ws(), gitagent.HookSetOptions{
			Task: "t-1", Attempt: 1, Workflow: wf,
			Judge: &judgeStub{verdict: `{"ok":true}`}, Depth: gitagent.MaxHookDepth,
		})
		Expect(v.Status).To(Equal(gitagent.StatusError))
		Expect(v.Findings[0].Message).To(ContainSubstring("H15"))
	})

	It("applies commit gates to changed paths", func() {
		w := ws()
		writeFileT(w.Dir, ".env", "TOKEN=x\n")
		w.Changed = append(w.Changed, ".env")
		v := gitagent.RunHookSet(ctx, w, gitagent.HookSetOptions{
			Task: "t-1", Attempt: 1, Tier: "sidecar",
			Workflow: &api.Workflow{Commits: []api.Commit{{}}},
		})
		Expect(v.Status).To(Equal(gitagent.StatusRejected))
		Expect(v.Findings[0].Kind).To(Equal("commit"))
		Expect(v.Findings[0].Message).To(ContainSubstring(".env"))
	})
})

var _ = Describe("verdict persistence (R6.9)", func() {
	It("round-trips a verdict keyed by task and attempt", func() {
		repo := GinkgoT().TempDir()
		v := gitagent.TierVerdict{
			V: 1, Task: "t-1", Attempt: 2, Status: gitagent.StatusRejected, Tier: "sidecar",
		}
		Expect(gitagent.SaveVerdict(repo, v)).To(Succeed())
		loaded, ok, err := gitagent.LoadVerdict(repo, "t-1", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(loaded.Status).To(Equal(gitagent.StatusRejected))

		_, ok, err = gitagent.LoadVerdict(repo, "t-1", 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("feedback wire format (§7)", func() {
	verdict := gitagent.TierVerdict{
		V: 1, Task: "t-1", Attempt: 2, Status: gitagent.StatusRejected, Tier: "sidecar",
		Findings: []gitagent.Finding{
			{Hook: "gate:path-denied", Kind: "commit", Path: ".env", Message: "path denied by policy"},
			{Hook: "verify:make lint", Kind: "exec", Feedback: "pkg/foo/bar.go:12:2: undefined: Baz\r\nsecond line"},
		},
	}

	It("renders the header, findings and a single captain-json line without CR", func() {
		block, err := gitagent.FormatFeedback(verdict, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(block).To(ContainSubstring("captain: REJECTED task t-1 attempt 2 (sidecar)"))
		Expect(block).To(ContainSubstring("✗ gate:path-denied"))
		Expect(block).To(ContainSubstring("undefined: Baz"))
		Expect(block).NotTo(ContainSubstring("\r"), "CR is eaten by the sideband demuxer (R7.1)")

		var jsonLine string
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "captain-json: ") {
				Expect(jsonLine).To(BeEmpty(), "exactly one captain-json line (R7.2)")
				jsonLine = strings.TrimPrefix(line, "captain-json: ")
			}
		}
		Expect(jsonLine).NotTo(BeEmpty())
		var decoded gitagent.TierVerdict
		Expect(json.Unmarshal([]byte(jsonLine), &decoded)).To(Succeed())
		Expect(decoded.Status).To(Equal(gitagent.StatusRejected))
		Expect(decoded.Findings).To(HaveLen(2))
	})

	It("caps the block at 64 KiB with a marker and log pointer (R7.3)", func() {
		big := verdict
		big.Findings = nil
		for range 24 {
			// Each finding's feedback is individually bounded, so overflow the
			// block with many findings rather than one enormous one.
			big.Findings = append(big.Findings, gitagent.Finding{
				Hook: "verify:test", Kind: "exec", Feedback: strings.Repeat("x", 9<<10),
			})
		}
		block, err := gitagent.FormatFeedback(big, "/var/log/captain/t-1.log")
		Expect(err).NotTo(HaveOccurred())
		Expect(len(block)).To(BeNumerically("<=", gitagent.MaxFeedbackBytes))
		Expect(block).To(ContainSubstring("[feedback truncated; full log: /var/log/captain/t-1.log]"))
		Expect(block).To(ContainSubstring("captain-json: "), "the JSON summary survives truncation")
	})
})
