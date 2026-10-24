package exectool_test

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credproxy"
)

const (
	awsSecretKey = "env-test-aws-secret"
	kubeToken    = "env-test-kube-token"
	grantSecret  = "env-test-grant-secret"
)

var awsConnection = connection.ExecConnections{AWS: &connection.AWSConnection{
	AccessKey: types.EnvVar{ValueStatic: "env-test-aws-access"},
	SecretKey: types.EnvVar{ValueStatic: awsSecretKey},
}}

// fakeKubernetes is an API server that answers only requests carrying its
// bearer token, and a kubeconfig holding that token — the real credential a
// proxied tool must never see.
func fakeKubernetes() (kubeconfig string, requests *[]string) {
	seen := &[]string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+kubeToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		*seen = append(*seen, r.URL.Path)
		_, _ = w.Write([]byte(`{"kind":"NamespaceList"}`))
	}))
	DeferCleanup(server.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	return fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: fake
clusters:
- name: fake
  cluster:
    server: %s
    certificate-authority-data: %s
contexts:
- name: fake
  context: {cluster: fake, user: fake}
users:
- name: fake
  user: {token: %s}
`, server.URL, base64.StdEncoding.EncodeToString(ca), kubeToken), seen
}

var _ = Describe("Inline tool environments", func() {
	var dir string

	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
	})

	run := func(setup *shell.Setup, tool api.InlineTool) func() (any, error) {
		_, definitions := bindAndPrepare(dir, exectool.WorkspaceOptions{Setup: setup}, api.InlineTools{"probe": tool})
		return func() (any, error) { return call(definitions[0], nil) }
	}

	It("refuses a call before the run has prepared the environment", func() {
		workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: dir})
		definitions, err := exectool.Definitions(api.InlineTools{"probe": {Exec: "true"}}, workspace)
		Expect(err).NotTo(HaveOccurred())

		_, err = call(definitions[0], nil)

		Expect(err).To(MatchError(ContainSubstring("not prepared")))
	})

	It("materializes connections once per run, outside the workspace, and removes them when the run ends", func() {
		workspace, definitions := bindAndPrepare(dir, exectool.WorkspaceOptions{},
			api.InlineTools{"probe": {Exec: credentialsFileScript, Setup: shell.Setup{Connections: awsConnection}}})

		first, err := call(definitions[0], nil)
		Expect(err).NotTo(HaveOccurred())
		second, err := call(definitions[0], nil)
		Expect(err).NotTo(HaveOccurred())

		credentials := decodePath(first)
		Expect(decodePath(second)).To(Equal(credentials), "one prepared environment serves every call")
		Expect(credentials).To(BeAnExistingFile())
		Expect(credentials).NotTo(HavePrefix(dir), "credential files stay out of the workspace")
		Expect(filepath.Join(dir, ".creds")).NotTo(BeAnExistingFile())

		Expect(workspace.Close()).To(Succeed())
		Expect(credentials).NotTo(BeAnExistingFile())
		_, err = call(definitions[0], nil)
		Expect(err).To(MatchError(ContainSubstring("not prepared")))
	})

	DescribeTable("hands over the run's credentials as the mode allows",
		func(credentials api.ToolCredentials, want string) {
			runSetup := &shell.Setup{Connections: awsConnection, EnvVars: []types.EnvVar{{Name: "REGION", ValueStatic: "eu-west-1"}}}
			result, err := run(runSetup, api.InlineTool{Credentials: credentials,
				Exec: `echo "${REGION:+region} [${AWS_SHARED_CREDENTIALS_FILE:+aws}]"`})()

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(want))
		},
		Entry("direct inherits the run's connection", api.ToolCredentialsDirect, "region [aws]"),
		Entry("an unset mode on the host is direct", api.ToolCredentials(""), "region [aws]"),
		Entry("none keeps only configuration", api.ToolCredentialsNone, "region []"),
		Entry("proxied does not inherit a connection no proxy can serve", api.ToolCredentialsProxied, "region []"),
	)

	It("refuses placement: sandbox when the run has no sandbox to confine it", func() {
		_, err := exectool.Definitions(api.InlineTools{"probe": {Exec: "true", Placement: api.ToolPlacementSandbox, Credentials: api.ToolCredentialsNone}},
			exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: dir}))

		Expect(err).To(MatchError(ContainSubstring("no sandbox wrapper")))
	})

	Context("proxied", func() {
		It("reaches kubernetes through a proxy whose kubeconfig holds no credential", func() {
			kubeconfig, requests := fakeKubernetes()
			runSetup := &shell.Setup{Connections: connection.ExecConnections{Kubernetes: &connection.KubernetesConnection{
				KubeconfigConnection: connection.KubeconfigConnection{Kubeconfig: &types.EnvVar{ValueStatic: kubeconfig}},
			}}}
			listNamespaces := run(runSetup, api.InlineTool{Credentials: api.ToolCredentialsProxied, Exec: `
server=$(sed -n 's/^ *server: //p' "$KUBECONFIG")
if grep -q "` + kubeToken + `" "$KUBECONFIG"; then echo leaked; exit 1; fi
curl -fsS "$server/api/v1/namespaces"`})

			result, err := listNamespaces()

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(`{"kind":"NamespaceList"}`))
			Expect(*requests).To(Equal([]string{"/api/v1/namespaces"}))
		})

		It("points the script at the egress proxy with placeholders and the run CA, never the secret", func() {
			grant := credproxy.Grant{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"}, Paths: []string{"/repos/acme/"},
				Headers: []credproxy.HeaderGrant{{Name: "Authorization", Value: types.EnvVar{ValueStatic: grantSecret}}}}
			result, err := run(nil, api.InlineTool{Grants: []credproxy.Grant{grant}, Exec: `env; echo "CA=$(head -1 "$SSL_CERT_FILE")"`})()

			Expect(err).NotTo(HaveOccurred())
			env := result.(string)
			Expect(env).To(ContainSubstring("GH_AUTHORIZATION=" + grant.Placeholder("Authorization")))
			Expect(env).To(MatchRegexp(`(?m)^HTTPS_PROXY=http://127\.0\.0\.1:\d+$`))
			Expect(env).To(ContainSubstring("CA=-----BEGIN CERTIFICATE-----"))
			Expect(env).NotTo(ContainSubstring(grantSecret))
		})

		It("refuses a tunnel to a destination no grant names", func() {
			grant := credproxy.Grant{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"}, Paths: []string{"/"}}
			result, err := run(nil, api.InlineTool{Grants: []credproxy.Grant{grant},
				Exec: `curl -s -o /dev/null -w '%{http_connect}' https://ungranted.example.com/ || true`})()

			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(result.(string))).To(Equal("403"))
		})
	})

	It("keeps the credentials directory private to the tool", func() {
		result, err := run(nil, api.InlineTool{Exec: credentialsFileScript, Setup: shell.Setup{Connections: awsConnection}})()
		Expect(err).NotTo(HaveOccurred())

		private := decodePath(result)
		for !strings.HasPrefix(filepath.Base(private), "captain-tool-probe-") {
			Expect(private).NotTo(Equal(filepath.Dir(private)), "no captain-tool directory above the credentials file")
			private = filepath.Dir(private)
		}
		info, err := os.Stat(private)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})
})

// credentialsFileScript prints the AWS credentials file path base64-encoded:
// the shell runner redacts the path itself from output, as it does every value
// a connection adds to the environment.
const credentialsFileScript = `printf %s "$AWS_SHARED_CREDENTIALS_FILE" | base64`

func decodePath(output any) string {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(output.(string)))
	Expect(err).NotTo(HaveOccurred())
	return string(decoded)
}
