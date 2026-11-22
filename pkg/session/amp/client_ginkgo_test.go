package amp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAmp(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Amp HTTP Suite")
}

var _ = Describe("Amp HTTP threads", func() {
	const threadID = "T-7f395a45-7fae-4983-8de0-d02e61d30183"
	const owner = "user-owner"
	const token = "example-token"
	var (
		client *Client
		serve  http.HandlerFunc
	)

	BeforeEach(func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			Expect(r.Header.Get("Authorization")).To(Equal("Bearer " + token))
			serve(w, r)
		}))
		DeferCleanup(server.Close)
		client = New(token)
		client.baseURL = server.URL
	})

	It("pages through the account's threads", func(ctx SpecContext) {
		serve = func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/api/v2/user/threads"))
			if r.URL.Query().Get("cursor") == "" {
				fmt.Fprint(w, `{"threads":[{"id":"T-first","hostname":"workstation"}],"nextCursor":"next+page"}`)
				return
			}
			Expect(r.URL.Query().Get("cursor")).To(Equal("next+page"))
			fmt.Fprintf(w, `{"threads":[{"id":%q,"repositories":[{"url":"https://github.com/example/project"}]}]}`, threadID)
		}
		threads, err := client.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(threads).To(Equal([]Thread{{ID: "T-first", Hostname: "workstation"}, {ID: threadID, Repositories: []Repository{{URL: "https://github.com/example/project"}}}}))
	})

	DescribeTable("checks the identity and eligibility of a pasted target", func(ctx SpecContext, fields, refusal string) {
		serve = func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v2/actor":
				fmt.Fprintf(w, `{"actor":{"id":%q}}`, owner)
			case "/api/v2/threads/" + threadID + "/metadata":
				fmt.Fprintf(w, `{"id":%q,%s}`, threadID, fields)
			default:
				Fail("unexpected request " + r.URL.Path)
			}
		}
		thread, err := client.OwnedOrb(ctx, threadID)
		if refusal != "" {
			Expect(err).To(MatchError(ContainSubstring(refusal)))
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(thread.ID).To(Equal(threadID))
	},
		Entry("owned", `"creatorUserID":"user-owner"`, ""),
		Entry("shared", `"creatorUserID":"someone-else"`, "does not own"),
		Entry("archived", `"creatorUserID":"user-owner","archived":true`, "archived"),
		Entry("local or runner", `"creatorUserID":"user-owner","hostname":"workstation"`, "Orb"),
	)

	DescribeTable("accepts only a matching Amp receipt", func(ctx SpecContext, status int, body, failure string) {
		const message = "Review the header\nCheck it on mobile"
		serve = func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Method).To(Equal(http.MethodPost))
			Expect(r.URL.Path).To(Equal("/api/v2/threads/" + threadID + "/messages"))
			var payload map[string]string
			Expect(json.NewDecoder(r.Body).Decode(&payload)).To(Succeed())
			Expect(payload).To(Equal(map[string]string{"message": message}))
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}
		receipt, err := client.Send(ctx, threadID, message)
		if failure != "" {
			Expect(err).To(MatchError(ContainSubstring(failure)))
			Expect(err.Error()).NotTo(ContainSubstring("private transcript"))
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(receipt).To(Equal("M-accepted"))
	},
		Entry("accepted", 202, `{"thread":{"id":"`+threadID+`"},"messageID":"M-accepted","assistant":"private transcript"}`, ""),
		Entry("wrong thread", 202, `{"thread":{"id":"T-other"},"messageID":"M-accepted"}`, "unconfirmed"),
		Entry("dropped response", 202, `{"thread":`, "unconfirmed"),
		Entry("non-Orb or missing", 404, `{"error":"private transcript"}`, "owned Orb"),
	)

	DescribeTable("normalizes only canonical thread references", func(value, id string) {
		parsed, err := ParseThreadRef(value)
		if id == "" {
			Expect(err).To(HaveOccurred())
			return
		}
		Expect(err).NotTo(HaveOccurred())
		Expect(parsed).To(Equal(id))
	},
		Entry("ID", threadID, threadID),
		Entry("URL", "https://ampcode.com/threads/"+threadID, threadID),
		Entry("wrong host", "https://example.com/threads/"+threadID, ""),
		Entry("additional path", "https://ampcode.com/threads/"+threadID+"/more", ""),
		Entry("query", "https://ampcode.com/threads/"+threadID+"?key=value", ""),
	)
})
