package query

import (
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
)

func metadataWithFiles(read, written []string) []byte {
	raw, err := json.Marshal(session.Metadata{Files: session.ChangedFiles{Read: read, Written: written}})
	Expect(err).NotTo(HaveOccurred())
	return raw
}

var _ = Describe("descendantFiles", func() {
	// ProjectionFacts.ThreadFiles is nil-vs-set on purpose: a leaf keeps its own
	// file set, while a session that spawned sub-agents reports the whole
	// thread's output. Collapsing the two hides who touched what.
	var parent, child, grandchild uuid.UUID

	BeforeEach(func() {
		parent, child, grandchild = uuid.New(), uuid.New(), uuid.New()
	})

	It("reports nothing for a session with no descendants", func() {
		thread := []database.SessionOverview{{ID: parent}}

		Expect(descendantFiles(parent, thread)).To(BeNil())
	})

	It("unions every descendant, transitively", func() {
		thread := []database.SessionOverview{
			{ID: parent},
			{ID: child, ParentSessionID: &parent, Metadata: metadataWithFiles([]string{"a.go"}, []string{"w1.go"})},
			{ID: grandchild, ParentSessionID: &child, Metadata: metadataWithFiles([]string{"b.go"}, []string{"w2.go"})},
		}

		files := descendantFiles(parent, thread)

		Expect(files).NotTo(BeNil())
		Expect(files.Read).To(Equal([]string{"a.go", "b.go"}))
		Expect(files.Written).To(Equal([]string{"w1.go", "w2.go"}))
	})

	It("excludes the session's own files, which the aggregate already holds", func() {
		// The union is descendants-only; merging with the session's own set is
		// the contributor's job, so doing it here too would be a second opinion.
		thread := []database.SessionOverview{
			{ID: parent, Metadata: metadataWithFiles([]string{"own.go"}, nil)},
			{ID: child, ParentSessionID: &parent, Metadata: metadataWithFiles([]string{"child.go"}, nil)},
		}

		files := descendantFiles(parent, thread)

		Expect(files.Read).To(Equal([]string{"child.go"}))
	})

	It("deduplicates files two descendants both touched", func() {
		other := uuid.New()
		thread := []database.SessionOverview{
			{ID: parent},
			{ID: child, ParentSessionID: &parent, Metadata: metadataWithFiles([]string{"same.go"}, nil)},
			{ID: other, ParentSessionID: &parent, Metadata: metadataWithFiles([]string{"same.go"}, nil)},
		}

		Expect(descendantFiles(parent, thread).Read).To(Equal([]string{"same.go"}))
	})
})
