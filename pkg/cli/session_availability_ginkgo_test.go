package cli

import (
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky/rpc"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Session detail availability", func() {
	It("retains identifying metadata without reporting an empty transcript as available", func(ctx SpecContext) {
		id := uuid.New()
		cwd, title := "/work/example", "Session metadata"
		store := &sessionGetOverviewStore{identity: []database.SessionOverview{{
			ID: id, Source: "claude", HostID: "example-host", CWD: &cwd, Title: &title,
			LifecycleStatus: "created", ActivityState: "idle",
		}}}
		result, err := runSessionGet(ctx, store, SessionGetOptions{ID: id.String()})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Sessions).To(HaveLen(1))
		item := result.Sessions[0]
		Expect(item.Detail).NotTo(BeNil())
		Expect(item.Detail.ID).To(Equal(id.String()))
		Expect(item.Detail.CWD).To(Equal(cwd))
		Expect(item.Detail.Title).To(Equal(title))
		Expect(item.DetailAvailable).To(BeFalse())
		Expect(item.Summary.DetailAvailable).To(BeFalse())
		Expect(item.Detail.Messages).To(BeEmpty())
		Expect(item.Pretty().String()).To(ContainSubstring("Transcript: unavailable"))
	})

	It("prefers available content when duplicate resume candidates both retain metadata aggregates", func() {
		metadata := SessionGetItem{
			CaptainID: "metadata", ProviderSessionID: "example-provider-session", Detail: &session.Session{},
		}
		available := SessionGetItem{
			CaptainID: "available", ProviderSessionID: metadata.ProviderSessionID,
			DetailAvailable: true, Detail: &session.Session{Messages: []session.Message{{Role: "assistant"}}},
		}
		selected, err := selectResumeSession([]SessionGetItem{metadata, available})
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(Equal(available))
	})

	It("documents unified session fields and availability in the session read response", func() {
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}}
		addCaptainSessionReadPaths(spec)
		operation := spec.Paths["/api/captain/sessions/{id}"]["get"]
		schema := operation.Responses["200"].Content["application/json"].Schema
		Expect(schema.Properties).To(HaveKey("messages"))
		Expect(schema.Properties).To(HaveKey("cwd"))
		Expect(schema.Properties["detailAvailable"].Type).To(Equal("boolean"))
		Expect(schema.Required).To(ContainElements("detailAvailable", "detailSource"))
		Expect(operation.Responses).To(HaveKey("404"))
	})
})
