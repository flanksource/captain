package approval_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// scriptedPrompter answers each prompt with the next scripted choice and keeps
// every prompt it was shown.
type scriptedPrompter struct {
	mu      sync.Mutex
	choices []approval.TerminalChoice
	shown   []approval.TerminalPrompt
}

func (s *scriptedPrompter) ask(_ context.Context, prompt approval.TerminalPrompt) (approval.TerminalChoice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shown = append(s.shown, prompt)
	if len(s.choices) == 0 {
		return "", errors.New("scripted prompter ran out of choices")
	}
	choice := s.choices[0]
	s.choices = s.choices[1:]
	return choice, nil
}

func (s *scriptedPrompter) prompts() []approval.TerminalPrompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]approval.TerminalPrompt(nil), s.shown...)
}

func toolRequest(tool string, input map[string]any) api.ApprovalRequest {
	return api.ApprovalRequest{Kind: api.ApprovalKindTool, Tool: tool, Input: input, ToolUseID: "toolu_" + tool}
}

var _ = Describe("Terminal approval broker", func() {
	const tool = "apply_fixture_patch"
	input := map[string]any{"path": "fixtures/policy.yaml", "lines": 3}

	It("allows once and prompts again for the next call", func(ctx SpecContext) {
		prompter := &scriptedPrompter{choices: []approval.TerminalChoice{approval.ChoiceAllowOnce, approval.ChoiceDeny}}
		terminal := approval.NewTestTerminal(nil, prompter.ask)

		first, err := terminal.OnApproval(ctx, toolRequest(tool, input))
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(Equal(api.ApprovalDecision{Allow: true}))

		second, err := terminal.OnApproval(ctx, toolRequest(tool, input))
		Expect(err).NotTo(HaveOccurred())
		Expect(second.Allow).To(BeFalse())
		Expect(prompter.prompts()).To(HaveLen(2))
	})

	It("shows the tool, the kind and the input as indented JSON", func(ctx SpecContext) {
		prompter := &scriptedPrompter{choices: []approval.TerminalChoice{approval.ChoiceAllowOnce}}
		terminal := approval.NewTestTerminal(nil, prompter.ask)

		_, err := terminal.OnApproval(ctx, toolRequest(tool, input))
		Expect(err).NotTo(HaveOccurred())
		Expect(prompter.prompts()).To(ConsistOf(approval.TerminalPrompt{
			Tool:   tool,
			Kind:   api.ApprovalKindTool,
			Detail: "{\n  \"lines\": 3,\n  \"path\": \"fixtures/policy.yaml\"\n}",
			// A non-interruptible request cannot take "deny and stop".
			Choices: []approval.TerminalChoice{approval.ChoiceAllowOnce, approval.ChoiceAllowForRun, approval.ChoiceDeny},
		}))
	})

	It("clips a large input and says it did", func(ctx SpecContext) {
		prompter := &scriptedPrompter{choices: []approval.TerminalChoice{approval.ChoiceAllowOnce}}
		terminal := approval.NewTestTerminal(nil, prompter.ask)

		_, err := terminal.OnApproval(ctx, toolRequest(tool, map[string]any{"body": strings.Repeat("x", 3*approval.MaxPromptDetail)}))
		Expect(err).NotTo(HaveOccurred())
		detail := prompter.prompts()[0].Detail
		head, note, found := strings.Cut(detail, "\n… clipped")
		Expect(found).To(BeTrue(), detail)
		Expect(len(head)).To(Equal(approval.MaxPromptDetail))
		Expect(note).To(ContainSubstring("of %d bytes", 3*approval.MaxPromptDetail+len("{\n  \"body\": \"\"\n}")))
	})

	It("remembers allow-for-run per tool and stops prompting for it", func(ctx SpecContext) {
		prompter := &scriptedPrompter{choices: []approval.TerminalChoice{approval.ChoiceAllowForRun, approval.ChoiceDeny}}
		terminal := approval.NewTestTerminal(nil, prompter.ask)

		for range 3 {
			decision, err := terminal.OnApproval(ctx, toolRequest(tool, input))
			Expect(err).NotTo(HaveOccurred())
			Expect(decision).To(Equal(api.ApprovalDecision{Allow: true}))
		}
		other, err := terminal.OnApproval(ctx, toolRequest("drop_table", nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(other.Allow).To(BeFalse())
		Expect(prompter.prompts()).To(HaveLen(2))
	})

	It("denies with a message naming the tool", func(ctx SpecContext) {
		prompter := &scriptedPrompter{choices: []approval.TerminalChoice{approval.ChoiceDeny}}
		terminal := approval.NewTestTerminal(nil, prompter.ask)

		decision, err := terminal.OnApproval(ctx, toolRequest(tool, input))
		Expect(err).NotTo(HaveOccurred())
		Expect(decision).To(Equal(api.ApprovalDecision{
			Message: tool + " denied by the operator at the terminal",
		}))
	})

	It("denies and interrupts an interruptible request", func(ctx SpecContext) {
		prompter := &scriptedPrompter{choices: []approval.TerminalChoice{approval.ChoiceDenyAndStop}}
		terminal := approval.NewTestTerminal(nil, prompter.ask)
		request := toolRequest(tool, input)
		request.Interruptible = true

		decision, err := terminal.OnApproval(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(decision).To(Equal(api.ApprovalDecision{
			Message:   tool + " denied by the operator at the terminal",
			Interrupt: true,
		}))
		Expect(prompter.prompts()[0].Choices).To(ContainElement(approval.ChoiceDenyAndStop))
	})

	It("shows a plan approval as the plan text and offers no allow-for-run", func(ctx SpecContext) {
		prompter := &scriptedPrompter{choices: []approval.TerminalChoice{approval.ChoiceAllowOnce}}
		terminal := approval.NewTestTerminal(nil, prompter.ask)
		plan := "1. patch the fixture\n2. rerun it"

		decision, err := terminal.OnApproval(ctx, api.ApprovalRequest{
			Kind: api.ApprovalKindPlan, Tool: "ExitPlanMode", Plan: &api.TerminalPlan{Content: plan},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(decision.Allow).To(BeTrue())
		Expect(prompter.prompts()[0].Detail).To(Equal(plan))
		Expect(prompter.prompts()[0].Choices).To(Equal([]approval.TerminalChoice{approval.ChoiceAllowOnce, approval.ChoiceDeny}))
	})

	It("refuses a question it cannot answer, naming the kind", func(ctx SpecContext) {
		prompter := &scriptedPrompter{}
		terminal := approval.NewTestTerminal(nil, prompter.ask)

		_, err := terminal.OnApproval(ctx, api.ApprovalRequest{
			Kind: api.ApprovalKindQuestion, Tool: "AskUserQuestion",
			Questions: []api.TerminalQuestion{{Text: "Which fixture?"}},
		})
		Expect(err).To(MatchError(ContainSubstring("question")))
		Expect(prompter.prompts()).To(BeEmpty())
	})

	It("suspends live output around the prompt", func(ctx SpecContext) {
		var events []string
		suspend := func() func() {
			events = append(events, "suspend")
			return func() { events = append(events, "resume") }
		}
		terminal := approval.NewTestTerminal(suspend, func(context.Context, approval.TerminalPrompt) (approval.TerminalChoice, error) {
			events = append(events, "ask")
			return approval.ChoiceAllowOnce, nil
		})

		_, err := terminal.OnApproval(ctx, toolRequest(tool, input))
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(Equal([]string{"suspend", "ask", "resume"}))
	})

	It("aborts with the context's error when its deadline passes mid-prompt", func(ctx SpecContext) {
		// huh reports a killed program as its own timeout error, not ctx's.
		terminal := approval.NewTestTerminal(nil, func(ctx context.Context, _ approval.TerminalPrompt) (approval.TerminalChoice, error) {
			<-ctx.Done()
			return "", errors.New("huh: program was killed")
		})
		deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()

		_, err := terminal.OnApproval(deadline, toolRequest(tool, input))
		Expect(err).To(MatchError(context.DeadlineExceeded))
	})

	It("ends a request queued behind an open prompt when its own deadline passes", func(ctx SpecContext) {
		release := make(chan struct{})
		entered := make(chan struct{})
		terminal := approval.NewTestTerminal(nil, func(context.Context, approval.TerminalPrompt) (approval.TerminalChoice, error) {
			close(entered)
			<-release
			return approval.ChoiceAllowOnce, nil
		})
		first := make(chan error, 1)
		go func() {
			_, err := terminal.OnApproval(ctx, toolRequest(tool, input))
			first <- err
		}()
		Eventually(entered).Should(BeClosed())

		deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		_, err := terminal.OnApproval(deadline, toolRequest(tool, input))
		Expect(err).To(MatchError(context.DeadlineExceeded))

		close(release)
		Eventually(first).Should(Receive(BeNil()))
	})

	It("serialises concurrent requests so only one prompt is open at a time", func(ctx SpecContext) {
		const callers = 4
		var open, maxOpen, asked atomic.Int32
		terminal := approval.NewTestTerminal(nil, func(context.Context, approval.TerminalPrompt) (approval.TerminalChoice, error) {
			now := open.Add(1)
			defer open.Add(-1)
			for {
				seen := maxOpen.Load()
				if now <= seen || maxOpen.CompareAndSwap(seen, now) {
					break
				}
			}
			asked.Add(1)
			time.Sleep(10 * time.Millisecond)
			return approval.ChoiceAllowOnce, nil
		})

		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				_, err := terminal.OnApproval(ctx, toolRequest(tool, map[string]any{"caller": i}))
				Expect(err).NotTo(HaveOccurred())
			}()
		}
		wg.Wait()
		Expect(asked.Load()).To(Equal(int32(callers)))
		Expect(maxOpen.Load()).To(Equal(int32(1)))
	})

	It("refuses to build on stdin that is not a terminal", func() {
		reader, writer, err := os.Pipe()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(reader.Close)
		DeferCleanup(writer.Close)

		_, err = approval.NewTerminal(approval.TerminalOptions{In: reader, Out: writer})
		Expect(err).To(MatchError("approval: terminal broker needs a TTY on stdin"))

		_, err = approval.NewTerminal(approval.TerminalOptions{Out: writer})
		Expect(err).To(MatchError("approval: terminal broker needs a TTY on stdin"))
	})
})
