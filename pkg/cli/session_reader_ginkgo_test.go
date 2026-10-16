// Tests SessionReader: a host that keeps Captain's tables in its own database reads and follows
// sessions there, and is served nothing but those reads.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SessionReader", Ordered, func() {
	var (
		server    *httptest.Server
		sessionID string
	)

	BeforeAll(func(ctx SpecContext) {
		open := func(name string) *database.DB {
			handle := dbtest.ForGinkgo(dbtest.Options{Name: name})
			db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
			return db
		}
		// Captain's own configured database holds nothing: the reader must not look there.
		setCaptainDBForTest(open("captain_session_reader_configured"))
		DeferCleanup(func() { setCaptainDBForTest(nil) })
		hosts := open("captain_session_reader_host")

		stored, err := hosts.CreateOrGetSession(ctx, database.CreateSessionInput{Source: "aichat", Provider: "captain", HostID: captainHostID()})
		Expect(err).NotTo(HaveOccurred())
		sessionID = stored.ID.String()
		Expect(hosts.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: stored.ID, ProviderMessageID: "m-1", Role: "assistant",
			Parts: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		})).To(Succeed())

		mux := http.NewServeMux()
		mux.Handle("/api/captain/sessions/", http.StripPrefix("/api/captain/sessions", SessionReader(hosts)))
		server = httptest.NewServer(mux)
		DeferCleanup(server.Close)
	})

	It("reads a session from the database it is given", func(ctx SpecContext) {
		response := getSession(ctx, server.URL+"/api/captain/sessions/"+sessionID, "application/json")
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusOK), string(body))
		var detail session.Session
		Expect(json.Unmarshal(body, &detail)).To(Succeed())
		Expect(detail.Messages).To(HaveLen(1))
		Expect(detail.Messages[0].ID).To(Equal("m-1"))
	})

	It("follows a session in the database it is given", func(ctx SpecContext) {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		response := getSession(streamCtx, server.URL+"/api/captain/sessions/"+sessionID+"?follow=1", "")
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusOK))
		frames := readSSEFrames(response, 1)
		Expect(frames[0].event).To(Equal("entry"))
		var message session.Message
		Expect(json.Unmarshal([]byte(frames[0].data), &message)).To(Succeed())
		Expect(message.ID).To(Equal("m-1"))
	})

	It("serves none of the session handler's other routes", func() {
		response, err := http.Post(server.URL+"/api/captain/sessions/"+sessionID+"/tokens", "application/json", strings.NewReader(`{"method":"estimate"}`))
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
	})
})
