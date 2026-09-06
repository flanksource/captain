package aichat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

func TestEnforceApprovalRuntimeProfile(t *testing.T) {
	tests := []struct {
		name     string
		spec     api.Spec
		resolved api.ComposedSpec
		wantErr  string
	}{
		{
			name:     "persisted model is no longer allowed",
			spec:     api.Spec{Model: api.Model{Name: "gpt-5.6-sol"}},
			resolved: api.ComposedSpec{Constraints: api.RuntimeConstraints{Models: []string{"claude-sonnet-5"}}},
			wantErr:  `model "gpt-5.6-sol" is outside the current effective model catalog`,
		},
		{
			name: "current quota is exhausted",
			spec: api.Spec{Model: api.Model{Name: "gpt-5.6-sol"}},
			resolved: api.ComposedSpec{Constraints: api.RuntimeConstraints{Quotas: []api.UsageQuota{{
				Name: "monthly", Scope: api.SpecLayerUser, Layer: "claims", TokenLimit: 100, TokensUsed: 100,
			}}}},
			wantErr: `quota "monthly" from layer "claims" exhausted`,
		},
		{
			name: "changed default does not replace an allowed persisted model",
			spec: api.Spec{Model: api.Model{Name: "gpt-5.6-sol"}},
			resolved: api.ComposedSpec{
				Spec:        api.Spec{Model: api.Model{Name: "claude-sonnet-5"}},
				Constraints: api.RuntimeConstraints{Models: []string{"gpt-5.6-sol", "claude-sonnet-5"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enforceApprovalRuntimeProfile(tt.spec, tt.resolved)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("enforceApprovalRuntimeProfile() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("enforceApprovalRuntimeProfile() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestAwaitSuspendedSeedWaitsForTheInFlightAssistantMessage(t *testing.T) {
	store := NewMemoryThreadStore()
	thread, err := store.Create(context.Background(), "Approve")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	turnID := "8f4c9f2b-1f7a-4a04-9d2b-0f2b3c4d5e6f"
	if err := store.AppendMessage(context.Background(), thread.ID, UIMessage{
		ID: "user-approve", Role: "user", TurnID: turnID,
		Parts: []UIPart{{Type: "text", Text: "Approve the account update"}},
	}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	// The suspension becomes resumable before the stream finishes persisting the
	// assistant message it suspended on.
	go func() {
		time.Sleep(2 * suspendedSeedInterval)
		_ = store.AppendMessage(context.Background(), thread.ID, UIMessage{
			ID: turnID + "-assistant", Role: "assistant", TurnID: turnID,
			Parts: []UIPart{{Type: "dynamic-tool", ToolName: "accounts_edit", ToolCallID: "call-1", State: "approval-requested"}},
		})
	}()

	seed, err := awaitSuspendedSeed(context.Background(), store, thread.ID, turnID)
	if err != nil {
		t.Fatalf("awaitSuspendedSeed: %v", err)
	}
	if seed.ID != turnID+"-assistant" || seed.TurnID != turnID {
		t.Fatalf("awaitSuspendedSeed = %q (turn %q), want the suspended assistant message", seed.ID, seed.TurnID)
	}
}

func TestAwaitSuspendedSeedStopsWithItsContext(t *testing.T) {
	store := NewMemoryThreadStore()
	thread, err := store.Create(context.Background(), "Approve")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(2 * suspendedSeedInterval)
		cancel()
	}()

	if _, err := awaitSuspendedSeed(ctx, store, thread.ID, "turn-1"); err == nil {
		t.Fatal("awaitSuspendedSeed succeeded without a suspended assistant message")
	}
}
