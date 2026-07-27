package cli

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/database"
	clickyrpc "github.com/flanksource/clicky/rpc"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type infoSessionStoreStub struct {
	providerID        string
	providerOverviews []database.SessionOverview
	providerErr       error
	identities        []string
	identityOverviews []database.SessionOverview
	identityErr       error
	threadRoots       []uuid.UUID
	threadOverviews   []database.SessionOverview
}

func (s *infoSessionStoreStub) ListSessionOverviewsByProviderSessionID(_ context.Context, id string) ([]database.SessionOverview, error) {
	s.providerID = id
	return s.providerOverviews, s.providerErr
}

func (s *infoSessionStoreStub) ListSessionOverviewsByIdentity(_ context.Context, identity string) ([]database.SessionOverview, error) {
	s.identities = append(s.identities, identity)
	return s.identityOverviews, s.identityErr
}

func (s *infoSessionStoreStub) ListThreadSessionOverviews(_ context.Context, rootID uuid.UUID) ([]database.SessionOverview, error) {
	s.threadRoots = append(s.threadRoots, rootID)
	return s.threadOverviews, nil
}

var _ = Describe("captain info environment session", func() {
	DescribeTable("detects the active agent before discovery",
		func(env map[string]string, expected *EnvironmentSessionInfo) {
			actual := detectEnvironmentSession(func(key string) string { return env[key] })
			Expect(actual).To(Equal(expected))
		},
		Entry("codex thread", map[string]string{
			"CODEX_THREAD_ID": "codex-thread", "CLAUDE_CODE_SESSION_ID": "claude-session",
		}, &EnvironmentSessionInfo{Source: "codex", SessionID: "codex-thread", Marker: "CODEX_THREAD_ID"}),
		Entry("codex compatibility session", map[string]string{
			"CODEX_SESSION_ID": "codex-session",
		}, &EnvironmentSessionInfo{Source: "codex", SessionID: "codex-session", Marker: "CODEX_SESSION_ID"}),
		Entry("codex provider marker", map[string]string{
			"CODEX_SANDBOX": "seatbelt",
		}, &EnvironmentSessionInfo{Source: "codex", Marker: "CODEX_SANDBOX"}),
		Entry("claude code session", map[string]string{
			"CLAUDE_CODE_SESSION_ID": "claude-session",
		}, &EnvironmentSessionInfo{Source: "claude", SessionID: "claude-session", Marker: "CLAUDE_CODE_SESSION_ID"}),
		Entry("legacy claude session", map[string]string{
			"CLAUDE_SESSION_ID": "legacy-claude",
		}, &EnvironmentSessionInfo{Source: "claude", SessionID: "legacy-claude", Marker: "CLAUDE_SESSION_ID"}),
		Entry("claude provider marker", map[string]string{
			"CLAUDECODE": "1",
		}, &EnvironmentSessionInfo{Source: "claude", Marker: "CLAUDECODE"}),
		Entry("gemini session", map[string]string{
			"GEMINI_SESSION_ID": "gemini-session",
		}, &EnvironmentSessionInfo{Source: "gemini", SessionID: "gemini-session", Marker: "GEMINI_SESSION_ID"}),
		Entry("gemini provider marker", map[string]string{
			"GEMINI_CLI": "1",
		}, &EnvironmentSessionInfo{Source: "gemini", Marker: "GEMINI_CLI"}),
		Entry("captain session", map[string]string{
			"CAPTAIN_SESSION_ID": "055781c7-360a-4eb2-80be-452b3937fcfe",
		}, &EnvironmentSessionInfo{Source: "captain", SessionID: "055781c7-360a-4eb2-80be-452b3937fcfe", Marker: "CAPTAIN_SESSION_ID"}),
		Entry("false boolean markers", map[string]string{
			"CLAUDECODE": "0", "GEMINI_CLI": "false",
		}, nil),
		Entry("blank markers", map[string]string{
			"CODEX_THREAD_ID": " ", "CLAUDE_CODE_SESSION_ID": "", "CAPTAIN_SESSION_ID": " ",
		}, nil),
	)

	It("exports the active environment session detector", func() {
		for _, marker := range []string{
			"CODEX_THREAD_ID", "CODEX_SESSION_ID", "CODEX_SANDBOX",
			"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID", "CLAUDECODE",
			"GEMINI_SESSION_ID", "GEMINI_CLI", "CAPTAIN_SESSION_ID",
		} {
			GinkgoT().Setenv(marker, "")
		}
		GinkgoT().Setenv("CODEX_THREAD_ID", "codex-thread")

		Expect(CurrentEnvironmentSession()).To(Equal(&EnvironmentSessionInfo{
			Source: "codex", SessionID: "codex-thread", Marker: "CODEX_THREAD_ID",
		}))
	})

	DescribeTable("preserves explicit discovery flags",
		func(opts InfoOptions) { Expect(infoUsesEnvironment(opts)).To(BeFalse()) },
		Entry("all", InfoOptions{All: true}),
		Entry("claude", InfoOptions{Claude: true}),
		Entry("codex", InfoOptions{Codex: true}),
		Entry("path", InfoOptions{Path: "/repo"}),
	)

	It("uses the environment fast path for a bare invocation", func() {
		Expect(infoUsesEnvironment(InfoOptions{})).To(BeTrue())
	})

	It("confines generated RPC discovery to the server workspace", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		outside := GinkgoT().TempDir()
		request := httptest.NewRequest("POST", "/api/v1/info", nil)
		requestContext := clickyrpc.ContextWithRequest(ctx, request)

		_, err := runInfo(requestContext, InfoOptions{Path: outside}, infoRuntime{
			getenv: func(string) string { return "" },
			getwd:  func() (string, error) { return workspace, nil },
		})

		Expect(err).To(MatchError(ContainSubstring("escapes workspace root")))
	})

	It("rejects generated RPC discovery through a workspace symlink", func(ctx SpecContext) {
		workspace := GinkgoT().TempDir()
		outside := GinkgoT().TempDir()
		link := filepath.Join(workspace, "outside")
		Expect(os.Symlink(outside, link)).To(Succeed())
		request := httptest.NewRequest("POST", "/api/v1/info", nil)
		requestContext := clickyrpc.ContextWithRequest(ctx, request)

		_, err := runInfo(requestContext, InfoOptions{Path: link}, infoRuntime{
			getenv: func(string) string { return "" },
			getwd:  func() (string, error) { return workspace, nil },
		})

		Expect(err).To(MatchError(ContainSubstring("escapes workspace root")))
	})

	It("returns a provider-only session without opening the database", func(ctx SpecContext) {
		databaseOpened := false
		result, err := runInfo(ctx, InfoOptions{}, infoRuntime{
			getenv: func(key string) string {
				if key == "GEMINI_CLI" {
					return "1"
				}
				return ""
			},
			getwd: func() (string, error) { return "/repo", nil },
			openDB: func(context.Context) (infoSessionStore, error) {
				databaseOpened = true
				return nil, errors.New("unexpected database open")
			},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(databaseOpened).To(BeFalse())
		Expect(result.CurrentSession).To(Equal(&EnvironmentSessionInfo{
			Source: "gemini", Marker: "GEMINI_CLI", CaptainSessions: []SessionRecord{},
		}))
	})

	It("returns an empty Captain session list when an exact provider ID is not stored", func(ctx SpecContext) {
		store := &infoSessionStoreStub{}
		result, err := runInfo(ctx, InfoOptions{}, infoRuntime{
			getenv: func(key string) string {
				if key == "CODEX_THREAD_ID" {
					return "codex-thread"
				}
				return ""
			},
			getwd:  func() (string, error) { return "/repo", nil },
			openDB: func(context.Context) (infoSessionStore, error) { return store, nil },
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.CurrentSession.CaptainSessions).To(BeEmpty())
		Expect(result.CurrentSession.CaptainSessions).NotTo(BeNil())
	})

	It("returns every exact provider-session database match", func(ctx SpecContext) {
		firstID := uuid.MustParse("055781c7-360a-4eb2-80be-452b3937fcfe")
		secondID := uuid.MustParse("7ca78c55-e280-50ff-a19a-9f355a6fc55e")
		providerID := "019f7c25-9adf-7901-add9-8c46693472fb"
		store := &infoSessionStoreStub{providerOverviews: []database.SessionOverview{
			{ID: firstID, ProviderSessionID: &providerID, Source: "codex"},
			{ID: secondID, ProviderSessionID: &providerID, Source: "captain"},
		}}
		session := EnvironmentSessionInfo{Source: "codex", SessionID: providerID, Marker: "CODEX_THREAD_ID"}

		resolved, err := resolveEnvironmentSession(ctx, store, session)

		Expect(err).NotTo(HaveOccurred())
		Expect(store.providerID).To(Equal(providerID))
		Expect(resolved.CaptainSessions).To(HaveLen(2))
		Expect(resolved.CaptainSessions[0].Key).To(Equal(firstID.String()))
		Expect(resolved.CaptainSessions[1].Key).To(Equal(secondID.String()))
	})

	It("expands a Captain root session through the existing thread resolver", func(ctx SpecContext) {
		rootID := uuid.MustParse("055781c7-360a-4eb2-80be-452b3937fcfe")
		childID := uuid.MustParse("7ca78c55-e280-50ff-a19a-9f355a6fc55e")
		store := &infoSessionStoreStub{
			identityOverviews: []database.SessionOverview{{ID: rootID, Source: "captain"}},
			threadOverviews: []database.SessionOverview{
				{ID: rootID, Source: "captain"}, {ID: childID, RootSessionID: &rootID, Source: "codex"},
			},
		}
		session := EnvironmentSessionInfo{Source: "captain", SessionID: rootID.String(), Marker: "CAPTAIN_SESSION_ID"}

		resolved, err := resolveEnvironmentSession(ctx, store, session)

		Expect(err).NotTo(HaveOccurred())
		Expect(store.identities).To(Equal([]string{rootID.String()}))
		Expect(store.threadRoots).To(Equal([]uuid.UUID{rootID}))
		Expect(resolved.CaptainSessions).To(HaveLen(2))
	})

	It("fails loudly for an invalid Captain session ID", func(ctx SpecContext) {
		_, err := resolveEnvironmentSession(ctx, &infoSessionStoreStub{}, EnvironmentSessionInfo{
			Source: "captain", SessionID: "not-a-uuid", Marker: "CAPTAIN_SESSION_ID",
		})

		Expect(err).To(MatchError(ContainSubstring("CAPTAIN_SESSION_ID")))
	})

	It("propagates exact provider lookup failures", func(ctx SpecContext) {
		lookupErr := errors.New("database unavailable")
		_, err := resolveEnvironmentSession(ctx, &infoSessionStoreStub{providerErr: lookupErr}, EnvironmentSessionInfo{
			Source: "codex", SessionID: "codex-thread", Marker: "CODEX_THREAD_ID",
		})

		Expect(err).To(MatchError(ContainSubstring("database unavailable")))
	})
})
