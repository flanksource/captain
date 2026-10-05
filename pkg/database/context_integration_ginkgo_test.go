package database

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Provider context database", func() {
	It("projects native context, reported zero and unavailable snapshots through detail and list queries", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_native_context"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
		for _, snapshot := range []*api.ContextUsage{
			{UsedTokens: 89595, WindowTokens: 258400, FreePercent: 69},
			{WindowTokens: 258400, FreePercent: 100}, nil,
		} {
			identity := uuid.NewString()
			s, err := db.CreateOrGetSession(ctx, CreateSessionInput{ProviderSessionID: identity, Source: "captain", Provider: "openai", HostID: "context-test"})
			Expect(err).NotTo(HaveOccurred())
			turn, _, err := db.CreateChatTurn(ctx, CreateChatTurnInput{SessionID: s.ID, ProviderTurnID: identity + "-turn"})
			Expect(err).NotTo(HaveOccurred())
			run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{SessionID: s.ID, TurnID: &turn.ID})
			Expect(err).NotTo(HaveOccurred())
			callID, err := db.CreateChatModelCall(ctx, CreateChatModelCallInput{TurnID: turn.ID, PromptRunID: run.ID, Model: "example-model", Provider: "openai", Mode: "agent"})
			Expect(err).NotTo(HaveOccurred())
			Expect(db.FinishChatModelCall(ctx, FinishChatModelCallInput{ID: callID, Status: ModelCallStatusSucceeded, StopReason: "end_turn",
				Event: api.Event{Context: snapshot, Usage: &api.Usage{InputTokens: 660747, CacheReadTokens: 582144}},
			})).To(Succeed())
			detail, err := db.ListSessionOverviewsByProviderSessionID(ctx, identity)
			Expect(err).NotTo(HaveOccurred())
			Expect(detail).To(HaveLen(1))
			list, err := db.ListSessionSummaries(ctx, SessionListFilter{Query: identity})
			Expect(err).NotTo(HaveOccurred())
			Expect(list.Rows).To(HaveLen(1))
			fields := Fields{"ContextTokens": BeNil(), "ContextWindowTokens": BeNil(), "ContextFreePercent": BeNil()}
			if snapshot != nil {
				fields = Fields{"ContextTokens": PointTo(Equal(int64(snapshot.UsedTokens))), "ContextWindowTokens": PointTo(Equal(int64(snapshot.WindowTokens))), "ContextFreePercent": PointTo(Equal(snapshot.FreePercent))}
			}
			Expect(detail[0]).To(MatchFields(IgnoreExtras, fields))
			Expect(list.Rows[0]).To(MatchFields(IgnoreExtras, fields))
		}
	})
})
