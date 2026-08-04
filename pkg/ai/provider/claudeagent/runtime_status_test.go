package claudeagent

import (
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
)

type runtimeStatusCase struct {
	installed       bool
	localTsx        bool
	paths           map[string]string
	wantBinary      string
	wantProvisioner string
	wantDependency  string
}

var _ = Describe("Claude Agent runtime prerequisites", func() {
	DescribeTable("reports how the runtime can become ready",
		func(test runtimeStatusCase) {
			cacheDir := GinkgoT().TempDir()
			agentDir := filepath.Join(cacheDir, "captain", "claude-agent")
			if test.installed {
				writeInstalledSDK(agentDir)
			}
			localTsx := filepath.Join(agentDir, "node_modules", ".bin", "tsx")
			if test.localTsx {
				Expect(os.MkdirAll(filepath.Dir(localTsx), 0o755)).To(Succeed())
				Expect(os.WriteFile(localTsx, []byte("tsx"), 0o755)).To(Succeed())
			}
			got := probeRuntimeStatus(runtimeProbeOptions{
				cacheDir: cacheDir,
				lookPath: func(binary string) (string, error) {
					if path := test.paths[binary]; path != "" {
						return path, nil
					}
					return "", os.ErrNotExist
				},
				readFile: os.ReadFile,
				stat:     os.Stat,
			})
			wantBinary := test.wantBinary
			if wantBinary == "local" {
				wantBinary = localTsx
			}
			Expect(got).To(Equal(ai.RuntimeStatus{
				Binary:            wantBinary,
				Provisioner:       test.wantProvisioner,
				DependencyMissing: test.wantDependency,
			}))
		},
		Entry("uses the provisioner for a cold cache", runtimeStatusCase{
			paths: map[string]string{"npm": "/bin/npm"}, wantProvisioner: "/bin/npm",
		}),
		Entry("reports npm for a cold cache without a provisioner", runtimeStatusCase{
			wantDependency: "npm",
		}),
		Entry("uses the provisioned tsx", runtimeStatusCase{
			installed: true, localTsx: true, wantBinary: "local",
		}),
		Entry("uses a global tsx when the provisioned executable is missing", runtimeStatusCase{
			installed: true, paths: map[string]string{"tsx": "/bin/tsx"}, wantBinary: "/bin/tsx",
		}),
		Entry("reports tsx when installed dependencies are incomplete", runtimeStatusCase{
			installed: true, wantDependency: "tsx",
		}),
	)
})

func writeInstalledSDK(agentDir string) {
	version, err := requiredSDKVersion()
	Expect(err).NotTo(HaveOccurred())
	manifest := filepath.Join(agentDir, "node_modules", "@anthropic-ai", "claude-agent-sdk", "package.json")
	Expect(os.MkdirAll(filepath.Dir(manifest), 0o755)).To(Succeed())
	Expect(os.WriteFile(manifest, []byte(fmt.Sprintf(`{"version":%q}`, version)), 0o644)).To(Succeed())
}
