package session

import (
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

var _ = ginkgo.Describe("session metadata projection", func() {
	full := Metadata{
		Model:     "gpt-5-codex",
		Provider:  "openai",
		Files:     ChangedFiles{Read: []string{"a.go"}, Written: []string{"b.go", "c.go"}},
		Todos:     []tools.TodoItem{{Text: "ship it", Status: "pending"}},
		Approvals: ApprovalStats{Approved: 3, Denied: 1},
		Plan:      &Plan{Path: "/plans/p.md", Slug: "p"},
	}

	ginkgo.It("round-trips every key it writes", func() {
		raw, err := json.Marshal(full.Encode())
		Expect(err).NotTo(HaveOccurred())

		Expect(DecodeMetadata(raw)).To(Equal(full))
	})

	ginkgo.It("encodes nothing when no field is set, so the stored blob is left alone", func() {
		Expect(Metadata{}.Encode()).To(BeNil())
	})

	ginkgo.DescribeTable("omits a field the session never produced",
		func(metadata Metadata, absent string) {
			Expect(metadata.Encode()).NotTo(HaveKey(absent))
		},
		ginkgo.Entry("no files", Metadata{Model: "m"}, "files"),
		ginkgo.Entry("empty files", Metadata{Model: "m", Files: ChangedFiles{}}, "files"),
		ginkgo.Entry("no todos", Metadata{Model: "m"}, "todos"),
		ginkgo.Entry("no approvals", Metadata{Model: "m"}, "approvals"),
		ginkgo.Entry("zero approvals", Metadata{Model: "m", Approvals: ApprovalStats{}}, "approvals"),
		ginkgo.Entry("no plan", Metadata{Model: "m"}, "plan"),
	)

	ginkgo.It("ignores sibling keys written by other producers", func() {
		// The blob is merged server-side (metadata || ?::jsonb), so tags/links
		// written elsewhere must decode as absent rather than as an error.
		decoded := DecodeMetadata(json.RawMessage(
			`{"tags":["x"],"links":{"pr":"1"},"files":{"written":["b.go"]}}`))

		Expect(decoded.Files.Written).To(Equal([]string{"b.go"}))
		Expect(decoded.Plan).To(BeNil())
	})

	ginkgo.DescribeTable("decodes an unusable blob to the zero projection",
		func(raw string) {
			Expect(DecodeMetadata(json.RawMessage(raw))).To(Equal(Metadata{}))
		},
		ginkgo.Entry("empty", ``),
		ginkgo.Entry("empty object", `{}`),
		ginkgo.Entry("null", `null`),
		ginkgo.Entry("malformed", `{"files":`),
	)
})
