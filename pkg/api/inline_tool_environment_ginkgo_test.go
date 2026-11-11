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
			got, err := tool.ResolveCredentials(tool.Placement, run)
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
		Entry("a supervisor tool holds secrets directly, like a host tool",
			api.InlineTool{Placement: api.ToolPlacementSupervisor, Setup: kubeSetup}, shell.Setup{}, api.ToolCredentialsDirect, ""),
		Entry("a sidecar tool with secrets needs a choice",
			api.InlineTool{Placement: api.ToolPlacementSidecar, Setup: awsSetup}, shell.Setup{}, api.ToolCredentials(""), "proxied, sidecar or sidecar-proxied"),
		Entry("a sidecar tool never receives the supervisor's secrets directly",
			api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsDirect}, shell.Setup{}, api.ToolCredentials(""), "would send the supervisor's secrets"),
		Entry("sidecar credentials on the supervisor",
			api.InlineTool{Placement: api.ToolPlacementSupervisor, Credentials: api.ToolCredentialsSidecar}, shell.Setup{}, api.ToolCredentials(""), "needs placement: sidecar, not supervisor"),
		Entry("sidecar credentials under the default placement",
			api.InlineTool{Credentials: api.ToolCredentialsSidecarProxied}, shell.Setup{}, api.ToolCredentials(""), "not the sandbox's default"),
		Entry("a sidecar tool with its own cloud identity",
			api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecar, Setup: awsSetup}, shell.Setup{}, api.ToolCredentialsSidecar, ""),
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
		Entry("grants without the proxy", api.InlineTool{Credentials: api.ToolCredentialsDirect, Grants: []credproxy.Grant{githubGrant}}, "need credentials: proxied or sidecar-proxied"),
		Entry("grants with sidecar-held credentials", api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecar, Grants: []credproxy.Grant{githubGrant}}, "not sidecar"),
		Entry("an invalid grant", api.InlineTool{Grants: []credproxy.Grant{{Name: "gh", URL: "ftp://example.com"}}}, "must be http(s)"),
		Entry("sidecar-proxied with nothing to proxy", api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecarProxied}, "declares none"),
		Entry("sidecar-proxied with a kubernetes connection",
			api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecarProxied, Grants: []credproxy.Grant{githubGrant}, Setup: kubeSetup}, "use credentials: proxied"),
		Entry("sidecar-proxied with a secret env var",
			api.InlineTool{Placement: api.ToolPlacementSidecar, Credentials: api.ToolCredentialsSidecarProxied, Grants: []credproxy.Grant{githubGrant}, Setup: secretSetup}, "declare them as grants"),
		Entry("sidecar credentials on a host tool", api.InlineTool{Placement: api.ToolPlacementHost, Credentials: api.ToolCredentialsSidecar}, "needs placement: sidecar"),
	)

	It("accepts static configuration under none", func() {
		Expect(api.InlineTools{"t": {Exec: "true", Credentials: api.ToolCredentialsNone, Setup: staticSetup}}.Validate()).To(Succeed())
	})

	DescribeTable("declares where each sandbox can run inline tools",
		func(kind api.SandboxKind, placement, want api.ToolPlacement, failure string) {
			descriptor, ok := api.SandboxFor(kind)
			Expect(ok).To(BeTrue())
			got, err := descriptor.ResolveToolPlacement(placement)
			if failure != "" {
				Expect(err).To(MatchError(ContainSubstring(failure)))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("off runs tools on the host by default", api.SandboxOff, api.ToolPlacement(""), api.ToolPlacementHost, ""),
		Entry("native runs tools on the host", api.SandboxNative, api.ToolPlacementHost, api.ToolPlacementHost, ""),
		Entry("native has no wrapper to place a tool in", api.SandboxNative, api.ToolPlacementSandbox, api.ToolPlacement(""), `it supports: host`),
		Entry("native has no sidecar", api.SandboxNative, api.ToolPlacementSidecar, api.ToolPlacement(""), `it supports: host`),
		Entry("docker cannot reach host tools yet", api.SandboxDocker, api.ToolPlacementHost, api.ToolPlacement(""), `"docker" cannot run inline tools`),
		Entry("git-agent runs tools on the supervisor by default", api.SandboxGitAgent, api.ToolPlacement(""), api.ToolPlacementSupervisor, ""),
		Entry("git-agent runs tools on the sidecar", api.SandboxGitAgent, api.ToolPlacementSidecar, api.ToolPlacementSidecar, ""),
		Entry("git-agent names its placements", api.SandboxGitAgent, api.ToolPlacementHost, api.ToolPlacement(""), `it supports: supervisor, sidecar`),
	)
})
