package verify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// withFactory registers a factory for the duration of one spec. The registry is
// process-global on purpose (a host links its fixture runner once at startup),
// so a spec that installs one must take it back out.
func withFactory(kind Kind, f Factory) func() {
	Register(kind, f)
	return func() { Unregister(kind) }
}

// recordingFactory captures what HooksFor handed it and yields one named plugin.
type recordingFactory struct {
	spec     api.Verify
	opts     Options
	calls    int
	verifier Verifier
}

func (r *recordingFactory) factory(_ context.Context, spec api.Verify, opts Options) ([]*Plugin, error) {
	r.calls++
	r.spec, r.opts = spec, opts
	v := r.verifier
	if v == nil {
		v = FuncVerifier(func(context.Context, string, []string) (Verdict, error) {
			return Verdict{OK: true}, nil
		})
	}
	return []*Plugin{New("fixture", v)}, nil
}

// progressStub reports five in-flight snapshots inside 100ms, so the emitter's
// coalescing window (500ms) is exercised by a verifier that reports faster than
// a reader can use.
type progressStub struct {
	progress func(api.VerifyReport)
	reported int
}

func (p *progressStub) SetProgress(fn func(api.VerifyReport)) { p.progress = fn }

func (p *progressStub) Verify(context.Context, string, []string) (Verdict, error) {
	for i := 1; i <= 5; i++ {
		snapshot := api.NewNodeReport(api.VerifyKindFunc, "fixture", api.VerifyNode{
			Name: fmt.Sprintf("check %d", i), Running: true,
		})
		p.progress(snapshot)
		p.reported++
		time.Sleep(20 * time.Millisecond)
	}
	final := api.NewNodeReport(api.VerifyKindFunc, "fixture", api.VerifyNode{Name: "check 5", Passed: true})
	return Verdict{OK: true, Report: &final}, nil
}

var _ = Describe("the verifier registry", func() {
	ctx := context.Background()

	It("yields no hooks for a workflow with nothing to verify", func() {
		hooks, err := HooksFor(ctx, nil, Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(hooks).To(BeNil())

		hooks, err = HooksFor(ctx, &api.Workflow{}, Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(hooks).To(BeNil())
	})

	It("refuses a declared fixture when no fixture verifier is registered", func() {
		Expect(Registered(KindFixture)).To(BeFalse(), "no fixture runner is linked in this test binary")

		_, err := HooksFor(ctx, &api.Workflow{Verify: &api.Verify{Fixture: "# acceptance\n"}}, Options{})
		Expect(err).To(MatchError(ContainSubstring("no fixture verifier is registered")))
	})

	It("hands the fixture document and the run's options to the registered factory", func() {
		recorder := &recordingFactory{}
		defer withFactory(KindFixture, recorder.factory)()

		wrap := func(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
			return cmd, args, env, nil
		}
		hooks, err := HooksFor(ctx, &api.Workflow{Verify: &api.Verify{Fixture: "# acceptance\n"}}, Options{
			Env: []string{"PATH=/usr/bin"}, Wrap: wrap, Timeout: 42 * time.Second,
			Progress: func(api.VerifyReport) {},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(hooks).To(HaveLen(1))
		Expect(recorder.calls).To(Equal(1))
		Expect(recorder.spec.Fixture).To(Equal("# acceptance\n"))
		Expect(recorder.opts.Env).To(Equal([]string{"PATH=/usr/bin"}))
		Expect(recorder.opts.Timeout).To(Equal(42 * time.Second))
		Expect(recorder.opts.Wrap).NotTo(BeNil())
		Expect(recorder.opts.Progress).NotTo(BeNil())
	})

	It("orders the hooks command, then prompt, then fixture", func() {
		defer withFactory(KindFixture, (&recordingFactory{}).factory)()
		promptPath := filepath.Join(GinkgoT().TempDir(), "judge.prompt")
		Expect(os.WriteFile(promptPath, []byte("{{role \"user\"}}\nJudge {{cwd}}."), 0o644)).To(Succeed())

		hooks, err := HooksFor(ctx, &api.Workflow{Verify: &api.Verify{
			Commands: []string{"go test ./...", "   "},
			Prompts:  []string{promptPath},
			Fixture:  "# acceptance\n",
		}}, Options{Provider: &judgeStubProvider{}})
		Expect(err).NotTo(HaveOccurred())

		names := make([]string, 0, len(hooks))
		for _, h := range hooks {
			names = append(names, h.(*Plugin).Name())
		}
		Expect(names).To(Equal([]string{"verify:go test ./...", "judge:" + promptPath, "fixture"}),
			"a blank command is skipped and the kinds keep their declared order")
	})

	It("applies the run's command environment and timeout to every command verifier", func() {
		hooks, err := HooksFor(ctx, &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}, Options{
			Env: []string{"PATH=/usr/bin"}, Timeout: 7 * time.Second,
		})
		Expect(err).NotTo(HaveOccurred())
		cv, ok := hooks[0].(*Plugin).Verifier().(*CmdVerifier)
		Expect(ok).To(BeTrue())
		Expect(cv.Env).To(Equal([]string{"PATH=/usr/bin"}))
		Expect(cv.Timeout).To(Equal(7 * time.Second))
	})

	It("refuses to register two factories for one kind", func() {
		Expect(func() { Register(KindCmd, (&recordingFactory{}).factory) }).To(PanicWith(
			ContainSubstring("cmd")))
	})

	It("lets a kind be taken back out and re-registered", func() {
		Expect(Unregister(KindFixture)).To(BeFalse(), "nothing claims the fixture kind yet")

		Register(KindFixture, (&recordingFactory{}).factory)
		Expect(Registered(KindFixture)).To(BeTrue())
		Expect(Unregister(KindFixture)).To(BeTrue())
		Expect(Registered(KindFixture)).To(BeFalse())

		// Register panics on a live kind, so unregistering is what makes a
		// replacement possible at all.
		Expect(func() { Register(KindFixture, (&recordingFactory{}).factory) }).NotTo(Panic())
		Expect(Unregister(KindFixture)).To(BeTrue())
	})

	It("coalesces a verifier's progress into at most one event per window, last snapshot always delivered", func() {
		var sunk []api.VerifyReport
		defer withFactory(KindFixture, (&recordingFactory{verifier: &progressStub{}}).factory)()

		var streamed []ai.Event
		hooks, err := HooksFor(ctx, &api.Workflow{Verify: &api.Verify{Fixture: "# acceptance\n"}}, Options{
			Progress: func(r api.VerifyReport) { sunk = append(sunk, r) },
		})
		Expect(err).NotTo(HaveOccurred())

		runner := &agent.Runner[string]{
			Provider:      &silentProvider{},
			Cwd:           GinkgoT().TempDir(),
			Request:       ai.Request{Prompt: api.Prompt{User: "fix it"}},
			Hooks:         hooks,
			MaxIterations: 1,
			OnEvent: func(_ int, ev ai.Event) {
				switch ev.Kind {
				case api.EventVerifyProgress, api.EventVerified, api.EventVerifyFailed:
					streamed = append(streamed, ev)
				}
			},
		}
		_, err = runner.Run(ctx)
		Expect(err).NotTo(HaveOccurred())

		var progress []ai.Event
		for _, ev := range streamed {
			if ev.Kind == api.EventVerifyProgress {
				progress = append(progress, ev)
			}
		}
		Expect(len(progress)).To(BeNumerically(">=", 1))
		Expect(len(progress)).To(BeNumerically("<=", 2), "five snapshots inside one 500ms window collapse")
		Expect(streamed[len(streamed)-1].Kind).To(Equal(api.EventVerified),
			"the last snapshot is flushed before the verdict, never after it")

		last, ok := progress[len(progress)-1].Raw.(*api.VerifyReport)
		Expect(ok).To(BeTrue(), "Raw carries the *api.VerifyReport")
		Expect(last.Tests[0].Name).To(Equal("check 5"), "the final snapshot always reaches the reader")
		Expect(progress[len(progress)-1].Tool).To(Equal("fixture"))

		Expect(sunk).To(HaveLen(len(progress)), "the Options sink sees the same coalesced snapshots")
		Expect(sunk[len(sunk)-1].Tests[0].Name).To(Equal("check 5"))
	})
})
