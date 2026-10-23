package claudeagent

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Agent steering", func() {
	// The SDK folds prompts that arrive mid-turn into the running turn and
	// reports one result naming every prompt it consumed; the captain turn ends
	// on the idle that follows it, not after one result per prompt.
	It("ends a turn whose steered prompts the SDK folded into one result", func() {
		withFakeAgentProcessEnv(GinkgoT(), map[string]string{fakeServerEnv: "1", fakeModeEnv: "steer-folded"})
		p, err := New(ai.Config{Model: api.Model{Name: "claude-sonnet-5"}})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(p.Close)

		events, err := p.ExecuteStream(context.Background(), ai.Request{Prompt: api.Prompt{User: "first"}})
		Expect(err).NotTo(HaveOccurred())
		Eventually(events).Should(Receive(HaveField("Kind", ai.EventText)))
		for _, text := range []string{"second", "third"} {
			Expect(p.Steer(context.Background(), ai.Request{Prompt: api.Prompt{User: text}})).To(Succeed())
		}

		ended := make(chan []ai.Event, 1)
		go func() {
			var results []ai.Event
			for event := range events {
				if event.Kind == ai.EventResult {
					results = append(results, event)
				}
			}
			ended <- results
		}()
		var results []ai.Event
		Eventually(ended, 5*time.Second).Should(Receive(&results), "the turn must end once the folded result settles every prompt")
		Expect(results).To(HaveLen(1))
		Expect(results[0].Input).To(HaveKeyWithValue("result", "folded prompts complete"))
	})
})
