// Specs for the bash provider: the prompt runs as a command in the run's directory, its output
// is the answer, streamed as it is written, and its exit code is the run's structured output.
package bash

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("the bash provider", func() {
	var provider *Provider
	var dir string
	BeforeEach(func() {
		var err error
		provider, err = New(ai.Config{Model: api.Model{Name: "bash"}})
		Expect(err).NotTo(HaveOccurred())
		dir = GinkgoT().TempDir()
	})

	request := func(command string) ai.Request {
		req := ai.Request{Prompt: api.Prompt{User: command}}
		req.SetCwd(dir)
		return req
	}

	It("answers with the command's output, and its exit code as the run's structured output", func() {
		response, err := provider.Execute(context.Background(), request("echo out; echo err >&2; exit 3"))
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Text).To(Equal("out\nerr\n"))
		Expect(response.StructuredData).To(MatchJSON(`{"exitCode":3}`))
		Expect(response.Model).To(Equal("bash"))
	})

	It("runs the command in the run's directory", func() {
		response, err := provider.Execute(context.Background(), request("pwd"))
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Text).To(Equal(dir + "\n"))
		Expect(response.StructuredData).To(MatchJSON(`{"exitCode":0}`))
	})

	It("streams the output as the command writes it, and ends with whether it exited 0", func() {
		events, err := provider.ExecuteStream(context.Background(), request("echo first; sleep 0.2; echo second; exit 1"))
		Expect(err).NotTo(HaveOccurred())
		var texts []string
		var result *ai.Event
		for event := range events {
			switch event.Kind {
			case ai.EventText:
				texts = append(texts, event.Text)
			case ai.EventResult:
				held := event
				result = &held
			}
		}
		Expect(texts).To(Equal([]string{"first\n", "second\n"}))
		Expect(result).NotTo(BeNil())
		Expect(result.Success).To(BeFalse())
		Expect(result.StructuredData).To(MatchJSON(`{"exitCode":1}`))
	})

	It("stops the command when the run is cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		events, err := provider.ExecuteStream(ctx, request("echo started; sleep 30"))
		Expect(err).NotTo(HaveOccurred())
		Eventually(events).Should(Receive(HaveField("Text", "started\n")))
		cancel()
		var last ai.Event
		Eventually(func() bool {
			event, open := <-events
			if open {
				last = event
			}
			return open
		}, 5*time.Second).Should(BeFalse())
		Expect(last.Kind).To(Equal(ai.EventError))
		Expect(last.Error).To(ContainSubstring("context canceled"))
	})

	It("refuses a run without a command", func() {
		_, err := provider.Execute(context.Background(), request("  "))
		Expect(err).To(MatchError("bash runs the prompt as a command, and the prompt is empty"))
	})
})
