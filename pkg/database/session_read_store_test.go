package database

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSessionConflictErrorListsEveryMatch(t *testing.T) {
	err := &SessionConflictError{
		Identity: "ad4c854e",
		Matches: []SessionIdentityMatch{
			{ID: uuid.MustParse("055781c7-360a-4eb2-80be-452b3937fcfe"), ProviderSessionID: "ad4c854e-cde6-4b99-99f3-667bf74112e3", Source: "claude", Project: "flanksource", Path: "/sessions/ad4c854e.jsonl"},
			{ID: uuid.MustParse("7ca78c55-e280-50ff-a19a-9f355a6fc55e"), ProviderSessionID: "ad4c854e-cde6-4b99-99f3-667bf74112e3", Source: "gavel"},
		},
	}
	if !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("error = %v, want ErrSessionConflict", err)
	}
	for _, expected := range []string{
		"session ID prefix \"ad4c854e\" matches 2 sessions",
		"055781c7-360a-4eb2-80be-452b3937fcfe",
		"7ca78c55-e280-50ff-a19a-9f355a6fc55e",
		"ad4c854e-cde6-4b99-99f3-667bf74112e3",
		"claude", "gavel", "flanksource", "/sessions/ad4c854e.jsonl",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error missing %q: %s", expected, err)
		}
	}
}
