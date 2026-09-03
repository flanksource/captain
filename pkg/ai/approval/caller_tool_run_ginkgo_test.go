package approval_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// callerToolRun is the other shape a broker is handed: an aichat turn, with the
// model call it was raised inside and the caller-tool credential the tool would
// run under. Its approvals are identified by all four columns, and stay alive
// only as long as that credential does.
type callerToolRun struct {
	*providerRun

	turn       uuid.UUID
	modelCall  uuid.UUID
	credential uuid.UUID
}

func newCallerToolRun(ctx context.Context, db *database.DB) *callerToolRun {
	GinkgoHelper()
	session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
		ID: uuid.New(), Source: "aichat", Provider: "anthropic",
	})
	Expect(err).NotTo(HaveOccurred())

	turn, _, err := db.CreateChatTurn(ctx, database.CreateChatTurnInput{
		SessionID: session.ID, ProviderTurnID: "turn-" + uuid.NewString(),
	})
	Expect(err).NotTo(HaveOccurred())

	run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{
		SessionID: session.ID, TurnID: &turn.ID,
	})
	Expect(err).NotTo(HaveOccurred())

	modelCall, err := db.CreateChatModelCall(ctx, database.CreateChatModelCallInput{
		TurnID: turn.ID, PromptRunID: run.ID,
		Model: "claude-sonnet-5", Provider: "anthropic", Mode: string(api.ModeAPI),
	})
	Expect(err).NotTo(HaveOccurred())

	credential, err := db.CreateCallerToolCredential(ctx, database.CreateCallerToolCredentialInput{
		SessionID: session.ID, PromptRunID: run.ID, Provider: "anthropic", Mode: api.ModeAPI,
		SecretHash: uniqueSecretHash(),
		Policy:     map[string]api.ToolPolicy{"Bash": api.ToolPolicyAsk},
	})
	Expect(err).NotTo(HaveOccurred())

	return &callerToolRun{
		providerRun: &providerRun{
			db: db, session: session.ID, run: run.ID, events: make(chan api.Event, 4),
		},
		turn: turn.ID, modelCall: modelCall, credential: credential.ID,
	}
}

// uniqueSecretHash is the 32 bytes a credential is keyed on. The column is
// unique, so two credentials in one suite cannot share a constant.
func uniqueSecretHash() []byte {
	first, second := uuid.New(), uuid.New()
	return append(first[:], second[:]...)
}

// callerBroker names all three identity columns, which is what makes the row a
// caller-tool approval rather than a provider one.
func (r *callerToolRun) callerBroker() *approval.Broker {
	return &approval.Broker{
		DB: r.db, SessionID: r.session, PromptRunID: r.run,
		TurnID: &r.turn, ModelCallID: &r.modelCall, CredentialID: r.credential,
		RequestedBy: "caller_tool", Timeout: approval.CallerToolTimeout, Poll: brokerPoll,
		Notify: r.notify, OnWaiting: r.markWaiting, OnRunning: r.markRunning,
	}
}
