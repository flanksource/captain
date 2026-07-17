package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/aichat"
)

func TestFileThreadStorePersistsThreads(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "threads.json")
	store := newFileThreadStore(path)

	thread, err := store.Create(ctx, "Launch cleanup")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if thread.ID == "" {
		t.Fatal("Create returned empty thread id")
	}

	if err := store.SetProviderSession(ctx, thread.ID, "provider-session-1"); err != nil {
		t.Fatalf("SetProviderSession: %v", err)
	}
	msg := aichat.UIMessage{
		Role:  "user",
		Parts: []aichat.UIPart{{Type: "text", Text: "continue"}},
	}
	if err := store.AppendMessage(ctx, thread.ID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	updated, err := store.AddUsage(ctx, thread.ID, aichat.TurnUsage{
		InputTokens:  10,
		OutputTokens: 5,
		CostUSD:      0.25,
	})
	if err != nil {
		t.Fatalf("AddUsage: %v", err)
	}
	if updated.ProviderSessionID != "provider-session-1" {
		t.Fatalf("ProviderSessionID = %q", updated.ProviderSessionID)
	}

	reloaded := newFileThreadStore(path)
	got, err := reloaded.Get(ctx, thread.ID)
	if err != nil {
		t.Fatalf("Get reloaded: %v", err)
	}
	if got.Title != "Launch cleanup" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.ProviderSessionID != "provider-session-1" {
		t.Errorf("ProviderSessionID = %q", got.ProviderSessionID)
	}
	if len(got.Messages) != 1 || got.Messages[0].Parts[0].Text != "continue" {
		t.Errorf("Messages = %+v", got.Messages)
	}
	if got.TotalInputTokens != 10 || got.TotalOutputTokens != 5 || got.TotalCostUSD != 0.25 {
		t.Errorf("usage totals = input %d output %d cost %f", got.TotalInputTokens, got.TotalOutputTokens, got.TotalCostUSD)
	}

	list, err := reloaded.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != thread.ID {
		t.Fatalf("List = %+v", list)
	}

	if err := reloaded.Delete(ctx, thread.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := reloaded.Get(ctx, thread.ID); err == nil {
		t.Fatal("Get deleted thread returned nil error")
	}
}
