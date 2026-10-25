package exectool_test

import (
	"context"
	"fmt"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credproxy"
)

var _ = Describe("Sidecar tools", func() {
	var dir string
	BeforeEach(func() { dir = GinkgoT().TempDir() })

	It("resolves credentials: sidecar from the tool's own declaration and inherits nothing from the run", func() {
		runSetup := &shell.Setup{EnvVars: []types.EnvVar{{Name: "RUN_REGION", ValueStatic: "supervisor-only"}}}
		tool := api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecar,
			Exec: credentialsFileScript, Setup: shell.Setup{Connections: awsConnection}}
		_, definitions := bindAndPrepare(dir, exectool.WorkspaceOptions{Setup: runSetup}, api.InlineTools{"probe": tool})

		result, err := call(definitions[0], nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(decodePath(result)).NotTo(BeEmpty(), "the tool's own aws connection was materialized")

		_, definitions = bindAndPrepare(dir, exectool.WorkspaceOptions{Setup: runSetup}, api.InlineTools{"probe": {
			Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecar, Exec: `echo "run=[${RUN_REGION:-}]"`}})
		result, err = call(definitions[0], nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("run=[]"))
	})

	Context("credentials: sidecar-proxied", func() {
		declared := credproxy.Grant{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"}, Paths: []string{"/"},
			Headers: []credproxy.HeaderGrant{{Name: "Authorization"}}}
		tool := api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecarProxied,
			Grants: []credproxy.Grant{declared}, Exec: `env`}

		It("serves the released values from a proxy here, while the script sees only placeholders", func() {
			var released []string
			release := func(_ context.Context, name string, grants []credproxy.Grant) ([]credproxy.Grant, error) {
				released = append(released, name)
				filled := []credproxy.Grant{grants[0]}
				filled[0].Headers = []credproxy.HeaderGrant{{Name: "Authorization", Value: types.EnvVar{ValueStatic: grantSecret}}}
				return filled, nil
			}
			_, definitions := bindAndPrepare(dir, exectool.WorkspaceOptions{ReleaseGrants: release}, api.InlineTools{"probe": tool})

			result, err := call(definitions[0], nil)

			Expect(err).NotTo(HaveOccurred())
			Expect(released).To(Equal([]string{"probe"}), "released once, when the environment was prepared")
			env := result.(string)
			Expect(env).To(ContainSubstring("GH_AUTHORIZATION=" + declared.Placeholder("Authorization")))
			Expect(env).To(MatchRegexp(`(?m)^HTTPS_PROXY=http://captain:[0-9a-f]{64}@127\.0\.0\.1:\d+$`))
			Expect(env).NotTo(ContainSubstring(grantSecret))
		})

		It("fails to prepare when the release fails, rather than run the tool without its credentials", func() {
			workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: dir,
				ReleaseGrants: func(context.Context, string, []credproxy.Grant) ([]credproxy.Grant, error) {
					return nil, fmt.Errorf("release already spent")
				}})
			_, err := exectool.Definitions(api.InlineTools{"probe": tool}, workspace)
			Expect(err).NotTo(HaveOccurred())

			Expect(workspace.Prepare(context.Background())).To(MatchError(ContainSubstring("release already spent")))
		})

		It("cannot be prepared outside a sidecar", func() {
			workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: dir})
			_, err := exectool.Definitions(api.InlineTools{"probe": tool}, workspace)
			Expect(err).NotTo(HaveOccurred())

			Expect(workspace.Prepare(context.Background())).To(MatchError(ContainSubstring("no supervisor to release the grants")))
		})
	})

	Context("credentials: proxied", func() {
		It("reaches kubernetes through the supervisor's proxy, holding neither the connection nor its token", func() {
			kubeconfig, requests := fakeKubernetes()
			declared := api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsProxied,
				Setup: shell.Setup{Connections: connection.ExecConnections{Kubernetes: &connection.KubernetesConnection{
					KubeconfigConnection: connection.KubeconfigConnection{Kubeconfig: &types.EnvVar{ValueStatic: kubeconfig}},
				}}}}
			endpoints, stopProxies, err := exectool.ServeToolProxies(context.Background(), declared, GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(stopProxies)

			shipped := declared
			shipped.Connections = connection.ExecConnections{}
			shipped.Exec = `
server=$(sed -n 's/^ *server: //p' "$KUBECONFIG")
token=$(sed -n 's/^ *token: //p' "$KUBECONFIG")
if grep -q "` + kubeToken + `" "$KUBECONFIG"; then echo leaked; exit 1; fi
echo "run=[${RUN_REGION:-}]"
curl -fsS -H "Authorization: Bearer $token" "$server/api/v1/namespaces"`
			var relayed, relayClosed []string
			supervisor := func(_ context.Context, name string) (exectool.ProxyEndpoints, func() error, error) {
				relayed = append(relayed, name)
				return endpoints, func() error { relayClosed = append(relayClosed, name); return nil }, nil
			}
			runSetup := &shell.Setup{EnvVars: []types.EnvVar{{Name: "RUN_REGION", ValueStatic: "supervisor-only"}}}
			workspace, definitions := bindAndPrepare(dir, exectool.WorkspaceOptions{Setup: runSetup, SupervisorProxies: supervisor},
				api.InlineTools{"probe": shipped})

			result, err := call(definitions[0], nil)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("run=[]\n" + `{"kind":"NamespaceList"}`))
			Expect(*requests).To(Equal([]string{"/api/v1/namespaces"}))
			Expect(relayed).To(Equal([]string{"probe"}))
			Expect(workspace.Close()).To(Succeed())
			Expect(relayClosed).To(Equal([]string{"probe"}), "the relay ends with the tool's environment")
		})

		It("cannot be prepared outside a sidecar", func() {
			workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: dir})
			_, err := exectool.Definitions(api.InlineTools{"probe": {Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsProxied,
				Grants: []credproxy.Grant{{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"}, Paths: []string{"/"}}}, Exec: `env`}}, workspace)
			Expect(err).NotTo(HaveOccurred())

			Expect(workspace.Prepare(context.Background())).To(MatchError(ContainSubstring("reaches no supervisor proxies")))
		})
	})
})
