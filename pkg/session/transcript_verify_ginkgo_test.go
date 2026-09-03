package session

import (
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/segmentio/encoding/json"
)

var _ = ginkgo.Describe("a stored verify verdict in the transcript", func() {
	report := func() []byte {
		r := api.NewNodeReport(api.VerifyKindCmd, "verify:go test ./...", api.VerifyNode{
			Name: "go test ./...", Failed: true, Message: "TestFoo failed",
		})
		r.Ran, r.Reason = true, "go test ./... failed"
		raw, err := json.Marshal(r)
		Expect(err).NotTo(HaveOccurred())
		return raw
	}

	// The verdict message carries the prose and the report side by side. The
	// data-verify part fell through to the default branch, which renders a part
	// type it does not know from its (empty) Text — one blank row per verdict.
	ginkgo.It("renders the report rather than an empty row", func() {
		s := &Session{Messages: []Message{{
			Role: RoleVerifyFailed,
			Parts: []Part{
				{Type: PartText, Text: "failed in 4ms — verify:go test ./..."},
				{Type: PartVerify, Data: report()},
			},
		}}}

		rows := s.TranscriptRows()
		Expect(rows).To(HaveLen(2))
		rendered := rows[1].Pretty().String()
		Expect(rendered).NotTo(BeEmpty())
		Expect(rendered).To(ContainSubstring("verify:go test ./..."))
		Expect(rendered).To(ContainSubstring("failed"))
	})

	// A part with no data at all is a row with nothing to say; emitting it is
	// the blank line this case exists to remove.
	ginkgo.It("emits no row for a verdict part with no report", func() {
		s := &Session{Messages: []Message{{
			Role:  RoleVerified,
			Parts: []Part{{Type: PartVerify}},
		}}}
		Expect(s.TranscriptRows()).To(BeEmpty())
	})
})
