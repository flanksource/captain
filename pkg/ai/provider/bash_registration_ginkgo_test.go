// Specs for the bash model's registration: "bash" resolves to the bash provider without a key,
// and a whole prompt run on it runs the prompt as a command and answers with its exit code.
package provider

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("bash model registration", func() {
	It("resolves bash to the bash provider, which needs no key", func() {
		provider, err := ai.NewProvider(ai.Config{Model: api.Model{Name: "bash"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(provider.GetRuntime()).To(Equal(ai.RuntimeOf(ai.Bash, ai.ModeAPI)))
		Expect(provider.GetModel()).To(Equal("bash"))
	})

	It("runs a prompt run's prompt as a command, answering with its output and exit code", func() {
		dir := GinkgoT().TempDir()
		spec := api.Spec{Model: api.Model{Name: "bash"}, Prompt: api.Prompt{User: "echo ran in $PWD; exit 4"}}
		spec.SetCwd(dir)
		result, err := promptrun.Run(context.Background(), promptrun.Input{
			Resolved: api.ResolvedSpec{Spec: spec},
			Timeout:  time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Response).NotTo(BeNil())
		Expect(result.Response.Text).To(Equal("ran in " + dir + "\n"))
		Expect(result.Response.StructuredData).To(MatchJSON(`{"exitCode":4}`))
	})
})
