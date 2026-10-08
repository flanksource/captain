package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/captain/pkg/database"
	sessiontokens "github.com/flanksource/captain/pkg/session/tokens"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Session token sizing database integration", func() {
	It("sizes canonical stored rows through HTTP and rejects stale revisions without changing the session", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_tokens"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		setCaptainDBForTest(db)
		DeferCleanup(func() { setCaptainDBForTest(nil); Expect(db.Close()).To(Succeed()) })
		stored, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
			Metadata: map[string]any{"model": "claude-sonnet-4-6"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: stored.ID, ProviderMessageID: "input", Role: "user", Parts: json.RawMessage(`[{"type":"text","text":"abcdefgh"}]`),
		})).To(Succeed())
		before, err := db.GetSession(ctx, stored.ID)
		Expect(err).NotTo(HaveOccurred())
		response := httptest.NewRecorder()
		SessionHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/"+stored.ID.String()+"/tokens", strings.NewReader(`{"method":"estimate","rowIds":["input-0"]}`)).WithContext(ctx))
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var result sessiontokens.Result
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.SessionID).To(Equal(stored.ID.String()))
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0].Error).To(BeEmpty())
		Expect(result.Rows[0].Size.Usage.InputTokens).To(Equal(2))
		Expect(result.Rows[0].Size.Coverage.Partial).To(BeTrue())
		response = httptest.NewRecorder()
		SessionHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/"+stored.ID.String()+"/tokens", strings.NewReader(`{"method":"estimate","revision":-1}`)).WithContext(ctx))
		Expect(response.Code).To(Equal(http.StatusConflict))
		updated, err := db.GetSession(ctx, stored.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Metadata).To(Equal(before.Metadata))
		Expect(updated.UpdatedAt).To(Equal(before.UpdatedAt))
	})
})
