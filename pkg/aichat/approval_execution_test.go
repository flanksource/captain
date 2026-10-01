package aichat

import (
	"context"
	"testing"
	"time"
)

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
