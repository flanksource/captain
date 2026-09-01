package api

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Unified sandbox settings", func() {
	It("round-trips one provider-neutral native policy", func() {
		required := true
		allowLocalBinding := false
		settings := SandboxRef{
			Mode: SandboxNative,
			Policy: &NativeSandboxPolicy{
				Required: &required,
				Filesystem: &SandboxFilesystemPolicy{
					Access:          SandboxFilesystemWorkspaceWrite,
					WritableRoots:   []string{"/workspace/cache"},
					DeniedReadRoots: []string{"~/.ssh"},
				},
				Network: &SandboxNetworkPolicy{
					Access:            SandboxNetworkRestricted,
					AllowedDomains:    []string{"registry.example.com"},
					AllowLocalBinding: &allowLocalBinding,
				},
			},
		}

		for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
			encoded, err := marshal(settings)
			Expect(err).NotTo(HaveOccurred())
			var decoded SandboxRef
			if marshalName(marshal) == "json" {
				Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
			} else {
				Expect(yaml.Unmarshal(encoded, &decoded)).To(Succeed())
			}
			Expect(decoded).To(Equal(settings))
		}
	})

	It("uses scalar shorthand only for an unconfigured mode", func() {
		encoded, err := yaml.Marshal(SandboxRef{Mode: SandboxNative})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(Equal("native\n"))

		encoded, err = yaml.Marshal(SandboxRef{Mode: SandboxNative, Backend: "pool"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring("mode: native"))
		Expect(string(encoded)).To(ContainSubstring("backend: pool"))
	})

	It("deep-merges policy for the same mode and replaces it when the mode changes", func() {
		base := Spec{Sandbox: &SandboxRef{
			Mode: SandboxNative,
			Policy: &NativeSandboxPolicy{
				Filesystem: &SandboxFilesystemPolicy{WritableRoots: []string{"/cache"}},
				Network:    &SandboxNetworkPolicy{AllowedDomains: []string{"example.com"}},
			},
		}}
		override := Spec{Sandbox: &SandboxRef{
			Mode: SandboxNative,
			Policy: &NativeSandboxPolicy{
				Network: &SandboxNetworkPolicy{DeniedDomains: []string{"uploads.example.com"}},
			},
		}}

		merged := base.Merge(override)
		Expect(merged.Sandbox.Policy.Filesystem.WritableRoots).To(Equal([]string{"/cache"}))
		Expect(merged.Sandbox.Policy.Network).To(Equal(&SandboxNetworkPolicy{
			AllowedDomains: []string{"example.com"}, DeniedDomains: []string{"uploads.example.com"},
		}))

		replaced := base.Merge(Spec{Sandbox: &SandboxRef{Mode: SandboxDocker, Backend: "build-pool"}})
		Expect(replaced.Sandbox).To(Equal(&SandboxRef{Mode: SandboxDocker, Backend: "build-pool"}))
	})

	It("rejects native policy outside native mode", func() {
		err := SandboxRef{
			Mode:   SandboxDocker,
			Policy: &NativeSandboxPolicy{Network: &SandboxNetworkPolicy{Access: SandboxNetworkRestricted}},
		}.Validate()
		Expect(err).To(MatchError(ContainSubstring("native policy requires sandbox mode native")))
	})
})

func marshalName(marshal func(any) ([]byte, error)) string {
	encoded, _ := marshal(map[string]string{"format": "probe"})
	if len(encoded) > 0 && encoded[0] == '{' {
		return "json"
	}
	return "yaml"
}
