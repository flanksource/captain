package cmux

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("cmux agent environment under permissions.access", func() {
	BeforeEach(func() {
		root := GinkgoT().TempDir()
		GinkgoT().Setenv("CODEX_HOME", filepath.Join(root, "user-codex"))
		GinkgoT().Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	})

	DescribeTable("starts read-only codex in the isolated CODEX_HOME, since the TUI has no --ignore-rules",
		func(agent string, access api.AccessLevel, wantHome bool) {
			env, err := agentEnv(agent, ai.Request{Permissions: api.Permissions{Access: access}})
			Expect(err).NotTo(HaveOccurred())
			if wantHome {
				Expect(env).To(HaveKeyWithValue("CODEX_HOME", HaveSuffix(filepath.Join("captain", "codex-read-only"))))
			} else {
				Expect(env).NotTo(HaveKey("CODEX_HOME"))
			}
		},
		Entry("read-only codex", "codex", api.AccessReadOnly, true),
		Entry("codex without access", "codex", api.AccessLevel(""), false),
		Entry("read-only claude", "claude", api.AccessReadOnly, false),
	)
})
