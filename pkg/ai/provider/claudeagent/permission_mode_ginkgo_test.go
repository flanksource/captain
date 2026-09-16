package claudeagent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude Agent permission mode switch", func() {
	var marker string

	BeforeEach(func() {
		marker = filepath.Join(GinkgoT().TempDir(), "marker")
		withFakeAgentProcessEnv(GinkgoT(), map[string]string{fakeServerEnv: "1", fakeMarkerEnv: marker})
	})

	startedProvider := func(cfg ai.Config) *Provider {
		provider, err := New(cfg)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = provider.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		events, err := provider.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{User: "hello"}})
		Expect(err).NotTo(HaveOccurred())
		for range events {
		}
		return provider
	}

	It("forwards the requested mode to the running SDK session", func() {
		provider := startedProvider(ai.Config{Model: api.Model{Name: testModel}})

		Expect(provider.SetPermissionMode(context.Background(), api.PermissionAuto)).To(Succeed())

		Expect(os.ReadFile(marker)).To(Equal([]byte("set_permission_mode auto")))
	})

	It("refuses before the SDK session has been initialized", func() {
		provider, err := New(ai.Config{Model: api.Model{Name: testModel}})
		Expect(err).NotTo(HaveOccurred())

		err = provider.SetPermissionMode(context.Background(), api.PermissionAuto)

		Expect(err).To(MatchError(ContainSubstring("not started")))
	})

	It("refuses an unrecognised mode without reaching the SDK session", func() {
		provider := startedProvider(ai.Config{Model: api.Model{Name: testModel}})

		err := provider.SetPermissionMode(context.Background(), api.PermissionMode("yolo"))

		Expect(err).To(MatchError(ContainSubstring(`"yolo"`)))
		Expect(marker).NotTo(BeAnExistingFile())
	})

	It("refuses bypassPermissions while tool approvals are brokered", func() {
		provider := startedProvider(ai.Config{
			Model: api.Model{Name: testModel},
			CanUseTool: func(context.Context, ai.PermissionRequest) (ai.PermissionDecision, error) {
				return ai.PermissionDecision{}, nil
			},
		})

		err := provider.SetPermissionMode(context.Background(), api.PermissionBypass)

		Expect(err).To(MatchError(ContainSubstring("brokered")))
		Expect(marker).NotTo(BeAnExistingFile())
	})
})
