package database

import (
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Session overview aggregates", func() {
	It("matches complete provider session IDs without including longer prefixes", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_provider_session_match"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		providerID := "019f7c25-9adf-7901-add9-8c46693472fb"
		for _, input := range []CreateSessionInput{
			{ID: uuid.New(), ProviderSessionID: providerID, Source: "codex", Provider: "openai", HostID: "host-a"},
			{ID: uuid.New(), ProviderSessionID: providerID, Source: "captain", Provider: "openai", HostID: "host-b"},
			{ID: uuid.New(), ProviderSessionID: providerID + "-child", Source: "codex", Provider: "openai", HostID: "host-a"},
		} {
			_, err = db.CreateOrGetSession(ctx, input)
			Expect(err).NotTo(HaveOccurred())
		}

		matches, err := db.ListSessionOverviewsByProviderSessionID(ctx, providerID)

		Expect(err).NotTo(HaveOccurred())
		Expect(matches).To(HaveLen(2))
		Expect(matches).To(ConsistOf(
			MatchFields(IgnoreExtras, Fields{"Source": Equal("codex"), "HostID": Equal("host-a")}),
			MatchFields(IgnoreExtras, Fields{"Source": Equal("captain"), "HostID": Equal("host-b")}),
		))
	})

	It("preserves every detail metric within its session security boundary", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_overview"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		rootID := uuid.MustParse("00000000-0000-0000-0000-000000000101")
		childID := uuid.MustParse("00000000-0000-0000-0000-000000000102")
		otherID := uuid.MustParse("00000000-0000-0000-0000-000000000201")
		for _, session := range []struct {
			id     uuid.UUID
			rootID *uuid.UUID
		}{
			{id: rootID},
			{id: childID, rootID: &rootID},
			{id: otherID},
		} {
			_, err = db.CreateOrGetSession(ctx, CreateSessionInput{
				ID: session.id, RootSessionID: session.rootID, Source: "codex",
				Provider: "openai", HostID: "overview-test",
			})
			Expect(err).NotTo(HaveOccurred())
		}

		turnOne := uuid.MustParse("00000000-0000-0000-0000-000000001101")
		turnTwo := uuid.MustParse("00000000-0000-0000-0000-000000001102")
		otherTurn := uuid.MustParse("00000000-0000-0000-0000-000000002101")
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_turns (id, session_id, turn_index, status)
			VALUES (?, ?, 0, 'ended'), (?, ?, 1, 'ended'), (?, ?, 0, 'ended')`,
			turnOne, rootID, turnTwo, rootID, otherTurn, otherID,
		).Error).NotTo(HaveOccurred())
		promptRunID := uuid.MustParse("00000000-0000-0000-0000-000000003101")
		checkpoint := []byte("provider-checkpoint-v1")
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_prompt_runs
			  (id, session_id, turn_id, root_session_id, state, phase, runtime, approval_state,
			   provider_checkpoint_codec, provider_checkpoint_version, provider_checkpoint)
			VALUES (?, ?, ?, ?, 'succeeded', 'finished', ?::jsonb, ?::jsonb, 'test-codec', 1, ?)`,
			promptRunID, rootID, turnOne, rootID, `{"mode":"run"}`, `{"decision":"allow"}`, checkpoint,
		).Error).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_prompt_runs (session_id, root_session_id, state, phase)
			VALUES (?, ?, 'failed', 'finished'), (?, ?, 'succeeded', 'finished')`,
			rootID, rootID, otherID, otherID,
		).Error).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_model_calls
			  (turn_id, call_index, prompt_run_id, model, provider, mode, effort, input_tokens, output_tokens,
			   reasoning_tokens, cache_read_tokens, cache_write_tokens, context_tokens,
			   context_window_tokens, input_cost, output_cost, reasoning_cost,
			   cache_read_cost, cache_write_cost, provider_cost_usd, currency, started_at, ended_at)
			VALUES
			  (?, 0, ?, 'model-old', 'openai', 'cli', 'low', 10, 4, 2, 3, 1, 20, 100,
			   0.10, 0.04, 0.02, 0.03, 0.01, 0.25, 'USD', '2026-07-16 10:00:00+00', '2026-07-16 10:01:00+00'),
			  (?, 0, ?, 'model-latest', 'openai', 'cli', 'high', 20, 8, 4, 6, 2, 75, 100,
			   0.20, 0.08, 0.04, 0.06, 0.02, 0, 'USD', '2026-07-16 11:00:00+00', '2026-07-16 11:01:00+00'),
			  (?, 1, ?, 'model-eur', 'openai', 'cli', NULL, 100, 50, 25, 10, 5, 50, 100,
			   9, 9, 9, 9, 9, 9, 'EUR', '2026-07-16 09:00:00+00', '2026-07-16 09:01:00+00')`,
			turnOne, promptRunID, turnTwo, promptRunID, turnTwo, promptRunID,
		).Error).NotTo(HaveOccurred())

		Expect(db.Gorm().Exec(`
			INSERT INTO captain_messages (session_id, sequence, role, parts)
			VALUES
			  (?, 0, 'assistant', ?::jsonb),
			  (?, 1, 'assistant', ?::jsonb),
			  (?, 0, 'assistant', ?::jsonb)`,
			rootID, `[{"type":"dynamic-tool","toolName":"shell"},{"type":"tool-read","toolName":"read"},{"type":"tool-empty"},{"type":"text"}]`,
			rootID, `{"type":"dynamic-tool","toolName":"ignored-non-array"}`,
			otherID, `[{"type":"tool-write","toolName":"write"}]`,
		).Error).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_events (session_id, kind)
			VALUES (?, 'assistant.delta'), (?, 'tool.completed'), (?, 'other-session')`,
			rootID, rootID, otherID,
		).Error).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_plans (source_session_id, title)
			VALUES (?, 'first'), (?, 'second'), (?, 'other')`,
			rootID, rootID, otherID,
		).Error).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_turn_requests (session_id, kind, state, resolved_at)
			VALUES
			  (?, 'question', 'pending', NULL),
			  (?, 'question', 'approved', now()),
			  (?, 'question', 'answered', now()),
			  (?, 'question', 'denied', now()),
			  (?, 'question', 'cancelled', now()),
			  (?, 'question', 'pending', NULL)`,
			rootID, rootID, rootID, rootID, rootID, otherID,
		).Error).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_artifacts (session_id, kind)
			VALUES
			  (?, 'file.read'), (?, 'file.write'), (?, 'file.edit'), (?, 'file.delete'),
			  (?, 'terminal.output'), (?, 'file.read')`,
			rootID, rootID, rootID, rootID, rootID, otherID,
		).Error).NotTo(HaveOccurred())

		var overview struct {
			MessageCount         int64
			ToolCallCount        int64
			EventCount           int64
			TurnCount            int64
			ModelCallCount       int64
			AgentCount           int64
			PromptRunCount       int64
			PlanCount            int64
			PendingRequestCount  int64
			ApprovedRequestCount int64
			DeniedRequestCount   int64
			FileReadCount        int64
			FileWrittenCount     int64
			Model                string
			ModelProvider        string
			ModelMode            string
			Effort               string
			ContextTokens        int64
			ContextWindowTokens  int64
			ContextFreePercent   int
			InputTokens          int64
			OutputTokens         int64
			ReasoningTokens      int64
			CacheReadTokens      int64
			CacheWriteTokens     int64
			TotalTokens          int64
			CostUSD              float64
		}
		Expect(db.Gorm().Raw(
			"SELECT * FROM captain_session_overview WHERE id = ?", rootID,
		).Scan(&overview).Error).NotTo(HaveOccurred())
		Expect(overview).To(Equal(struct {
			MessageCount         int64
			ToolCallCount        int64
			EventCount           int64
			TurnCount            int64
			ModelCallCount       int64
			AgentCount           int64
			PromptRunCount       int64
			PlanCount            int64
			PendingRequestCount  int64
			ApprovedRequestCount int64
			DeniedRequestCount   int64
			FileReadCount        int64
			FileWrittenCount     int64
			Model                string
			ModelProvider        string
			ModelMode            string
			Effort               string
			ContextTokens        int64
			ContextWindowTokens  int64
			ContextFreePercent   int
			InputTokens          int64
			OutputTokens         int64
			ReasoningTokens      int64
			CacheReadTokens      int64
			CacheWriteTokens     int64
			TotalTokens          int64
			CostUSD              float64
		}{
			MessageCount: 2, ToolCallCount: 2, EventCount: 2,
			TurnCount: 2, ModelCallCount: 3, AgentCount: 2,
			PromptRunCount: 2, PlanCount: 2,
			PendingRequestCount: 1, ApprovedRequestCount: 2, DeniedRequestCount: 1,
			FileReadCount: 1, FileWrittenCount: 3,
			Model: "model-latest", ModelProvider: "openai", ModelMode: "cli", Effort: "high",
			ContextTokens: 75, ContextWindowTokens: 100, ContextFreePercent: 25,
			InputTokens: 130, OutputTokens: 62, ReasoningTokens: 31,
			CacheReadTokens: 19, CacheWriteTokens: 8, TotalTokens: 250,
			CostUSD: 0.65,
		}))

		turns, err := db.ListThreadTurns(ctx, rootID)
		Expect(err).NotTo(HaveOccurred())
		var turnCost float64
		var turnTokens int64
		for _, turn := range turns {
			turnCost += turn.CostUSD
			turnTokens += turn.TotalTokens
		}
		agents, err := db.ListThreadAgents(ctx, rootID)
		Expect(err).NotTo(HaveOccurred())
		var agentCost float64
		var agentTokens int64
		for _, agent := range agents {
			agentCost += agent.CostUSD
			agentTokens += agent.TotalTokens
		}
		costs, err := db.ListThreadCosts(ctx, rootID)
		Expect(err).NotTo(HaveOccurred())
		var groupedCost float64
		var groupedTokens int64
		for _, cost := range costs {
			groupedTokens += cost.TotalTokens
			if cost.Currency == "USD" {
				groupedCost += cost.TotalCost
			}
		}
		page, err := db.ListSessionSummaries(ctx, SessionListFilter{RootsOnly: true, Limit: 10})
		Expect(err).NotTo(HaveOccurred())
		var listCost float64
		var listTokens int64
		for _, row := range page.Rows {
			if row.ID == rootID {
				listCost = row.CostUSD
				listTokens = row.TotalTokens
			}
		}

		var promptOverview struct {
			TurnID                    uuid.UUID
			Runtime                   string
			ProviderCheckpointCodec   string
			ProviderCheckpointVersion int
			TotalTokens               int64
			CostUSD                   float64
			ProviderCostUSD           float64
			InputCost                 float64
			OutputCost                float64
			ReasoningCost             float64
			CacheReadCost             float64
			CacheWriteCost            float64
		}
		Expect(db.Gorm().Raw(`
			SELECT turn_id, runtime::text AS runtime,
			  provider_checkpoint_codec, provider_checkpoint_version,
			  total_tokens, cost_usd, provider_cost_usd, input_cost, output_cost,
			  reasoning_cost, cache_read_cost, cache_write_cost
			FROM captain_prompt_run_overview
			WHERE id = ?`, promptRunID,
		).Scan(&promptOverview).Error).NotTo(HaveOccurred())
		Expect(promptOverview.TurnID).To(Equal(turnOne))
		Expect(promptOverview.Runtime).To(MatchJSON(`{"mode":"run"}`))
		Expect(promptOverview.ProviderCheckpointCodec).To(Equal("test-codec"))
		Expect(promptOverview.ProviderCheckpointVersion).To(Equal(1))
		Expect(promptOverview.TotalTokens).To(Equal(int64(250)), "reasoning belongs in prompt-run total tokens")
		Expect(promptOverview.ProviderCostUSD).To(BeNumerically("~", 0.25, 1e-9))
		Expect(promptOverview.InputCost).To(BeNumerically("~", 0.30, 1e-9))
		Expect(promptOverview.OutputCost).To(BeNumerically("~", 0.12, 1e-9))
		Expect(promptOverview.ReasoningCost).To(BeNumerically("~", 0.06, 1e-9))
		Expect(promptOverview.CacheReadCost).To(BeNumerically("~", 0.09, 1e-9))
		Expect(promptOverview.CacheWriteCost).To(BeNumerically("~", 0.03, 1e-9))
		var sensitiveColumnCount int64
		Expect(db.Gorm().Raw(`
			SELECT count(*)
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'captain_prompt_run_overview'
			  AND column_name IN ('approval_state', 'provider_checkpoint')
		`).Scan(&sensitiveColumnCount).Error).NotTo(HaveOccurred())
		Expect(sensitiveColumnCount).To(BeZero(), "private approval/checkpoint state must not be exposed by the view")

		for surface, cost := range map[string]float64{
			"session overview": overview.CostUSD,
			"session turns":    turnCost,
			"session agents":   agentCost,
			"session costs":    groupedCost,
			"session list":     listCost,
			"prompt run":       promptOverview.CostUSD,
		} {
			Expect(cost).To(BeNumerically("~", 0.65, 1e-9), surface)
		}
		for surface, tokens := range map[string]int64{
			"session overview": overview.TotalTokens,
			"session turns":    turnTokens,
			"session agents":   agentTokens,
			"session costs":    groupedTokens,
			"session list":     listTokens,
			"prompt run":       promptOverview.TotalTokens,
		} {
			Expect(tokens).To(Equal(int64(250)), surface)
		}

		var securityBarrier bool
		Expect(db.Gorm().Raw(`SELECT 'security_barrier=true' = ANY(COALESCE(reloptions, '{}')) FROM pg_class
			WHERE oid = 'public.captain_session_overview'::regclass`,
		).Scan(&securityBarrier).Error).NotTo(HaveOccurred())
		Expect(securityBarrier).To(BeTrue())
	})
})
