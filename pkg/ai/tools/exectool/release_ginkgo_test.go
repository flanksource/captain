package exectool_test

import (
	"context"

	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/credproxy"
)

const releasedSecret = "release-test-secret"

var _ = Describe("Credential release", func() {
	grant := credproxy.Grant{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"},
		Headers: []credproxy.HeaderGrant{{Name: "Authorization", Value: types.EnvVar{ValueStatic: releasedSecret}}}}
	// declared is the grant as it reaches the sidecar: the header's name only.
	declared := credproxy.Grant{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"},
		Headers: []credproxy.HeaderGrant{{Name: "Authorization"}}}

	serve := func() (string, string) {
		release, closeRelease, err := exectool.ServeGrantRelease(context.Background(), "probe", []credproxy.Grant{grant})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(closeRelease)
		return "http://" + release.Addr + "/", release.Token
	}

	It("fills the declared grants with the real values, once", func() {
		url, token := serve()

		filled, err := exectool.FetchGrantRelease(context.Background(), url, token, []credproxy.Grant{declared})

		Expect(err).NotTo(HaveOccurred())
		Expect(filled).To(Equal([]credproxy.Grant{{Name: "gh", URL: "https://api.example.com", Methods: []string{"GET"},
			Headers: []credproxy.HeaderGrant{{Name: "Authorization", Value: types.EnvVar{ValueStatic: releasedSecret}}}}}))
		_, err = exectool.FetchGrantRelease(context.Background(), url, token, []credproxy.Grant{declared})
		Expect(err).To(HaveOccurred(), "a second reader must get nothing")
	})

	It("refuses a reader without the release token, and stays available for the one that has it", func() {
		url, token := serve()

		_, err := exectool.FetchGrantRelease(context.Background(), url, "guessed", []credproxy.Grant{declared})
		Expect(err).To(MatchError(ContainSubstring("401")))

		_, err = exectool.FetchGrantRelease(context.Background(), url, token, []credproxy.Grant{declared})
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses a release that does not match what the tool declares", func() {
		url, token := serve()
		other := credproxy.Grant{Name: "jira", URL: "https://jira.example.com", Headers: []credproxy.HeaderGrant{{Name: "Authorization"}}}

		_, err := exectool.FetchGrantRelease(context.Background(), url, token, []credproxy.Grant{other})

		Expect(err).To(MatchError(ContainSubstring("no value for grant jira")))
	})
})
