package api_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/flanksource/captain/pkg/api"
)

// The approval window belongs beside permissions.mode because it answers the
// same question — how much authority a run holds on its own — and because a
// compile-time constant cannot tell an unattended CI run from a dashboard
// somebody is watching. These specs pin it to the layering machinery that
// already resolves mode, not to a parallel config path.
var _ = Describe("Permissions.ApprovalTimeout", func() {
	Describe("parsing", func() {
		It("reports no declared window when it is unset", func() {
			window, err := api.Permissions{}.ParseApprovalTimeout()
			Expect(err).NotTo(HaveOccurred())
			Expect(window).To(BeZero())
		})

		It("resolves a declared window to its duration", func() {
			window, err := api.Permissions{ApprovalTimeout: "45m"}.ParseApprovalTimeout()
			Expect(err).NotTo(HaveOccurred())
			Expect(window).To(Equal(45 * time.Minute))
		})

		DescribeTable("refuses a window that would never bound anything",
			func(value string) {
				_, err := api.Permissions{ApprovalTimeout: value}.ParseApprovalTimeout()
				Expect(err).To(MatchError(ContainSubstring(value)))
				Expect(api.Permissions{ApprovalTimeout: value}.Validate()).To(MatchError(ContainSubstring(value)))
			},
			Entry("unparseable", "half an hour"),
			Entry("zero", "0s"),
			Entry("negative", "-5m"),
		)
	})

	Describe("layering", func() {
		// Mode is the control: whatever precedence it follows, the window follows,
		// because they are resolved by the same merge and not by two code paths.
		It("lets a later layer override an earlier one, exactly as mode does", func() {
			config := api.Spec{Permissions: api.Permissions{
				Mode: api.PermissionPlan, ApprovalTimeout: "24h",
			}}
			frontmatter := api.Spec{Permissions: api.Permissions{
				Mode: api.PermissionAcceptEdits, ApprovalTimeout: "2h",
			}}
			request := api.Spec{Permissions: api.Permissions{ApprovalTimeout: "10m"}}

			resolved := config.Merge(frontmatter).Merge(request)
			Expect(resolved.Permissions.ApprovalTimeout).To(Equal("10m"))
			Expect(resolved.Permissions.Mode).To(Equal(api.PermissionAcceptEdits),
				"a layer that says nothing about mode must not reset it, and the window must behave the same way")
		})

		It("inherits the window from the layer below when no later layer names one", func() {
			config := api.Spec{Permissions: api.Permissions{ApprovalTimeout: "90m"}}
			resolved := config.Merge(api.Spec{Permissions: api.Permissions{Mode: api.PermissionPlan}})
			Expect(resolved.Permissions.ApprovalTimeout).To(Equal("90m"))
		})
	})

	Describe("wire shape", func() {
		It("round-trips through YAML under permissions", func() {
			encoded, err := yaml.Marshal(api.Spec{Permissions: api.Permissions{
				Mode: api.PermissionPlan, ApprovalTimeout: "30m",
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(string(encoded)).To(ContainSubstring("approvalTimeout: 30m"))

			var decoded api.Spec
			Expect(yaml.Unmarshal(encoded, &decoded)).To(Succeed())
			Expect(decoded.Permissions.ApprovalTimeout).To(Equal("30m"))
		})

		It("does not make an otherwise-empty permissions block look configured", func() {
			Expect(api.IsEmpty(api.Permissions{})).To(BeTrue())
			Expect(api.IsEmpty(api.Permissions{ApprovalTimeout: "30m"})).To(BeFalse())
		})
	})
})
