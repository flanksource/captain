package api

import (
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

// TestCommitDefaults pins the resolved defaults, which are what a one-key
// stanza like `commits: [{on: turn}]` actually means.
func TestCommitDefaults(t *testing.T) {
	cases := []struct {
		name       string
		commit     Commit
		wantPhase  CommitPhase
		wantMode   CommitMode
		wantSquash bool
		wantGates  CommitGates
	}{
		{
			name:       "empty stanza commits once at the end",
			commit:     Commit{},
			wantPhase:  CommitOnRun,
			wantMode:   CommitModeCommit,
			wantSquash: false,
			wantGates:  CommitGatesCheap,
		},
		{
			name:       "per-turn defaults to a squashed fixup chain",
			commit:     Commit{On: CommitOnTurn},
			wantPhase:  CommitOnTurn,
			wantMode:   CommitModeFixup,
			wantSquash: true,
			wantGates:  CommitGatesCheap,
		},
		{
			name:       "per-turn with squash off keeps the chain",
			commit:     Commit{On: CommitOnTurn, Squash: ptr(false)},
			wantPhase:  CommitOnTurn,
			wantMode:   CommitModeFixup,
			wantSquash: false,
			wantGates:  CommitGatesCheap,
		},
		{
			name:       "explicit plain commits per turn are never squashed",
			commit:     Commit{On: CommitOnTurn, Mode: CommitModeCommit},
			wantPhase:  CommitOnTurn,
			wantMode:   CommitModeCommit,
			wantSquash: false,
			wantGates:  CommitGatesCheap,
		},
		{
			name:       "gates are honoured verbatim",
			commit:     Commit{On: CommitOnAgent, Gates: CommitGatesNone},
			wantPhase:  CommitOnAgent,
			wantMode:   CommitModeCommit,
			wantSquash: false,
			wantGates:  CommitGatesNone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.commit.Phase(); got != tc.wantPhase {
				t.Errorf("Phase() = %q, want %q", got, tc.wantPhase)
			}
			if got := tc.commit.EffectiveMode(); got != tc.wantMode {
				t.Errorf("EffectiveMode() = %q, want %q", got, tc.wantMode)
			}
			if got := tc.commit.ShouldSquash(); got != tc.wantSquash {
				t.Errorf("ShouldSquash() = %v, want %v", got, tc.wantSquash)
			}
			if got := tc.commit.EffectiveGates(); got != tc.wantGates {
				t.Errorf("EffectiveGates() = %q, want %q", got, tc.wantGates)
			}
		})
	}
}

func TestCommitValidate(t *testing.T) {
	cases := []struct {
		name    string
		commit  Commit
		wantErr string
	}{
		{name: "empty is valid", commit: Commit{}},
		{name: "per-turn fixup is valid", commit: Commit{On: CommitOnTurn}},
		{name: "unknown phase", commit: Commit{On: "iteration"}, wantErr: "invalid commit phase"},
		{name: "unknown mode", commit: Commit{Mode: "rebase"}, wantErr: "invalid commit mode"},
		{name: "unknown when", commit: Commit{When: "onFailure"}, wantErr: "invalid commit when"},
		{name: "unknown stage", commit: Commit{Stage: "session"}, wantErr: "invalid commit stage"},
		{name: "unknown gates", commit: Commit{Gates: "some"}, wantErr: "invalid commit gates"},
		{
			// Silently ignoring squash on a non-fixup policy would leave the
			// author believing their chain collapses when nothing squashes it.
			name:    "squash without fixup is rejected",
			commit:  Commit{On: CommitOnRun, Squash: ptr(true)},
			wantErr: "squash requires mode fixup",
		},
		{
			name:    "anchor without fixup is rejected",
			commit:  Commit{On: CommitOnRun, Anchor: "HEAD~2"},
			wantErr: "anchor requires mode fixup",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.commit.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestWorkflowValidateReportsCommitIndex checks a bad stanza is attributable to
// the entry that caused it, not just to "the workflow".
func TestWorkflowValidateReportsCommitIndex(t *testing.T) {
	wf := &Workflow{Commits: []Commit{{On: CommitOnTurn}, {On: "whenever"}}}
	err := wf.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error for the second commit")
	}
	if !strings.Contains(err.Error(), "commits[1]") {
		t.Errorf("error should name the offending index, got: %v", err)
	}
}
