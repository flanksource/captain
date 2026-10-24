package api_test

import (
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credproxy"
)

var (
	kubeSetup   = shell.Setup{Connections: connection.ExecConnections{Kubernetes: &connection.KubernetesConnection{}}}
	awsSetup    = shell.Setup{Connections: connection.ExecConnections{AWS: &connection.AWSConnection{}}}
	secretSetup = shell.Setup{EnvVars: []types.EnvVar{{Name: "TOKEN", ValueFrom: &types.EnvVarSource{
		SecretKeyRef: &types.SecretKeySelector{LocalObjectReference: types.LocalObjectReference{Name: "vault"}, Key: "token"},
	}}}}
	staticSetup = shell.Setup{EnvVars: []types.EnvVar{{Name: "REGION", ValueStatic: "eu-west-1"}}, DotEnv: []string{".env"}}
	githubGrant = credproxy.Grant{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"}, Paths: []string{"/repos/acme/"},
		Headers: []credproxy.HeaderGrant{{Name: "Authorization", Value: types.EnvVar{ValueStatic: "token"}}}}
)

var _ = Describe("Inline tool credentials", func() {
	DescribeTable("derives or checks the mode against the run's setup",
		func(tool api.InlineTool, run shell.Setup, want api.ToolCredentials, failure string) {
			got, err := tool.ResolveCredentials(run)
			if failure != "" {
				Expect(err).To(MatchError(ContainSubstring(failure)))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("nothing secret anywhere: none", api.InlineTool{Setup: staticSetup}, staticSetup, api.ToolCredentialsNone, ""),
		Entry("own connection on the host: direct", api.InlineTool{Setup: kubeSetup}, shell.Setup{}, api.ToolCredentialsDirect, ""),
		Entry("inherited secret env on the host: direct", api.InlineTool{}, secretSetup, api.ToolCredentialsDirect, ""),
		Entry("inherited connection in the sandbox needs a choice",
			api.InlineTool{Placement: api.ToolPlacementSandbox}, kubeSetup, api.ToolCredentials(""), "needs an explicit credentials"),
		Entry("explicit none ignores what the run has",
			api.InlineTool{Credentials: api.ToolCredentialsNone}, awsSetup, api.ToolCredentialsNone, ""),
		Entry("proxied under a run with cloud connections stays proxied",
			api.InlineTool{Credentials: api.ToolCredentialsProxied}, awsSetup, api.ToolCredentialsProxied, ""),
		Entry("grants derive proxied", api.InlineTool{Grants: []credproxy.Grant{githubGrant}}, awsSetup, api.ToolCredentialsProxied, ""),
	)

	DescribeTable("rejects contradictory declarations",
		func(tool api.InlineTool, message string) {
			tool.Exec = "true"
			Expect(api.InlineTools{"t": tool}.Validate()).To(MatchError(ContainSubstring(message)))
		},
		Entry("an unknown placement", api.InlineTool{Placement: "remote"}, "invalid placement"),
		Entry("an unknown credentials mode", api.InlineTool{Credentials: "vault"}, "invalid credentials"),
		Entry("none with its own connection", api.InlineTool{Credentials: api.ToolCredentialsNone, Setup: kubeSetup}, "credentials: none"),
		Entry("none with a secret env var", api.InlineTool{Credentials: api.ToolCredentialsNone, Setup: secretSetup}, "credentials: none"),
		Entry("proxied with a cloud connection", api.InlineTool{Credentials: api.ToolCredentialsProxied, Setup: awsSetup}, "no proxy can serve (aws)"),
		Entry("proxied with a secret env var", api.InlineTool{Credentials: api.ToolCredentialsProxied, Setup: secretSetup}, "declare them as grants"),
		Entry("grants without the proxy", api.InlineTool{Credentials: api.ToolCredentialsDirect, Grants: []credproxy.Grant{githubGrant}}, "need credentials: proxied"),
		Entry("an invalid grant", api.InlineTool{Grants: []credproxy.Grant{{Name: "gh", URL: "ftp://example.com"}}}, "must be http(s)"),
	)

	It("accepts static configuration under none", func() {
		Expect(api.InlineTools{"t": {Exec: "true", Credentials: api.ToolCredentialsNone, Setup: staticSetup}}.Validate()).To(Succeed())
	})

	DescribeTable("declares where each sandbox can run inline tools",
		func(kind api.SandboxKind, placement api.ToolPlacement, failure string) {
			descriptor, ok := api.SandboxFor(kind)
			Expect(ok).To(BeTrue())
			err := descriptor.ValidateToolPlacement(placement)
			if failure == "" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(ContainSubstring(failure)))
			}
		},
		Entry("off runs tools on the host", api.SandboxOff, api.ToolPlacement(""), ""),
		Entry("native runs tools on the host", api.SandboxNative, api.ToolPlacementHost, ""),
		Entry("native has no wrapper to place a tool in", api.SandboxNative, api.ToolPlacementSandbox, `it supports: host`),
		Entry("docker cannot reach host tools yet", api.SandboxDocker, api.ToolPlacementHost, `"docker" cannot run inline tools`),
		Entry("git-agent has no route back to tools", api.SandboxGitAgent, api.ToolPlacementHost, `"git-agent" cannot run inline tools`),
	)
})
