package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A reader of a long live session loads its tail, follows only what comes
// after it, and pages older history on demand: ?tail=, ?follow=1&after= and
// ?before= are those three reads.
var _ = Describe("SessionHandler transcript paging", Ordered, func() {
	const messages = 6
	var (
		server  *httptest.Server
		pagedID string
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_paging"})
		db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		setCaptainDBForTest(db)
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})

		paged, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{Source: "aichat", Provider: "captain", HostID: captainHostID()})
		Expect(err).NotTo(HaveOccurred())
		pagedID = paged.ID.String()
		for i := range messages {
			Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
				SessionID: paged.ID, ProviderMessageID: fmt.Sprintf("p-%d", i), Role: "assistant",
				Parts: json.RawMessage(fmt.Sprintf(`[{"type":"text","text":"paged %d"}]`, i)),
			})).To(Succeed())
		}

		mux := http.NewServeMux()
		mux.Handle("/api/captain/sessions/", http.StripPrefix("/api/captain/sessions", SessionHandler()))
		server = httptest.NewServer(mux)
		DeferCleanup(server.Close)
	})

	read := func(ctx context.Context, query string) (int, []string, string) {
		GinkgoHelper()
		response := getSession(ctx, server.URL+"/api/captain/sessions/"+pagedID+query, "application/json")
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())
		if response.StatusCode != http.StatusOK {
			return response.StatusCode, nil, string(body)
		}
		var detail session.Session
		Expect(json.Unmarshal(body, &detail)).To(Succeed())
		ids := []string{}
		for _, message := range detail.Messages {
			ids = append(ids, message.ID)
		}
		return response.StatusCode, ids, string(body)
	}

	DescribeTable("windows the JSON read",
		func(ctx SpecContext, query string, want []string) {
			status, ids, body := read(ctx, query)
			Expect(status).To(Equal(http.StatusOK), body)
			Expect(ids).To(Equal(want))
		},
		Entry("the newest rows under ?tail=", "?tail=2", []string{"p-4", "p-5"}),
		Entry("the rows just before a message under ?before=&limit=", "?before=p-4&limit=2", []string{"p-2", "p-3"}),
		Entry("every earlier row under ?before= alone", "?before=p-2", []string{"p-0", "p-1"}),
		Entry("nothing before the first message", "?before=p-0&limit=2", []string{}),
	)

	DescribeTable("rejects a window it cannot honour with a JSON 400",
		func(ctx SpecContext, query, wantError string) {
			status, _, body := read(ctx, query)
			Expect(status).To(Equal(http.StatusBadRequest), body)
			Expect(sessionErrorText(body)).To(ContainSubstring(wantError))
		},
		Entry("a before cursor no message carries", "?before=p-missing", `no message "p-missing"`),
		Entry("before combined with tail", "?before=p-4&tail=2", "before cannot be combined with tail or offset"),
	)

	It("follows only the messages after ?after=, then the state", func(ctx SpecContext) {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		response := getSession(streamCtx, server.URL+"/api/captain/sessions/"+pagedID+"?follow=1&after=p-3", "")
		defer response.Body.Close()

		Expect(response.StatusCode).To(Equal(http.StatusOK))
		frames := readSSEFrames(response, 3)
		got := []string{}
		for _, frame := range frames[:2] {
			Expect(frame.event).To(Equal("entry"))
			var message session.Message
			Expect(json.Unmarshal([]byte(frame.data), &message)).To(Succeed())
			got = append(got, message.ID)
		}
		Expect(got).To(Equal([]string{"p-4", "p-5"}))
		Expect(frames[2].event).To(Equal("state"))
	})

	It("refuses an after cursor no message carries before the stream opens", func(ctx SpecContext) {
		response := getSession(ctx, server.URL+"/api/captain/sessions/"+pagedID+"?follow=1&after=p-missing", "")
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		Expect(err).NotTo(HaveOccurred())

		Expect(response.StatusCode).To(Equal(http.StatusBadRequest), string(body))
		Expect(response.Header.Get("Content-Type")).To(ContainSubstring("application/json"))
		Expect(sessionErrorText(string(body))).To(ContainSubstring(`no message "p-missing"`))
	})

	It("answers an unknown session id with 404, not a cursor error", func(ctx SpecContext) {
		response := getSession(ctx, server.URL+"/api/captain/sessions/"+uuid.NewString()+"?follow=1&after=p-3", "")
		defer response.Body.Close()
		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
	})
})

// sessionErrorText is the `error` field of a JSON error body.
func sessionErrorText(body string) string {
	GinkgoHelper()
	var decoded struct {
		Error string `json:"error"`
	}
	Expect(json.Unmarshal([]byte(body), &decoded)).To(Succeed(), body)
	return decoded.Error
}
