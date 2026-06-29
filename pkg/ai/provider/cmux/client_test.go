package cmux

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

type runnerCall struct {
	cwd    string
	binary string
	args   []string
}

type recordingRunner struct {
	calls []runnerCall
	out   map[string]string
}

func (r *recordingRunner) run(_ context.Context, cwd, binary string, _ time.Duration, args ...string) (string, error) {
	call := runnerCall{cwd: cwd, binary: binary, args: append([]string(nil), args...)}
	r.calls = append(r.calls, call)
	if r.out != nil {
		if stdout, ok := r.out[joinArgs(args)]; ok {
			return stdout, nil
		}
	}
	return "ok", nil
}

func TestParseWorkspaceRef(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want WorkspaceRef
	}{
		{
			name: "json",
			out:  `{"workspaceId":"ws-json","surfaceId":"sf-json"}`,
			want: WorkspaceRef{WorkspaceID: "ws-json", SurfaceID: "sf-json", Raw: `{"workspaceId":"ws-json","surfaceId":"sf-json"}`},
		},
		{
			name: "labeled text",
			out:  "created workspace=ws-text surface=sf-text",
			want: WorkspaceRef{WorkspaceID: "ws-text", SurfaceID: "sf-text", Raw: "created workspace=ws-text surface=sf-text"},
		},
		{
			name: "plain ref",
			out:  "ws-plain\n",
			want: WorkspaceRef{Raw: "ws-plain"},
		},
		{
			name: "cmux workspace ref",
			out:  "OK workspace:22\n",
			want: WorkspaceRef{WorkspaceID: "workspace:22", Raw: "OK workspace:22"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseWorkspaceRef(tc.out); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseWorkspaceRef() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseSurfaceRef(t *testing.T) {
	cases := map[string]string{
		`{"ref":"surface:json"}`:               "surface:json",
		"created surface=surface:text\n":       "surface:text",
		"surface:plain\n":                      "surface:plain",
		"cmux: notice\n{\"id\":\"surface:x\"}": "surface:x",
	}
	for input, want := range cases {
		if got := ParseSurfaceRef(input); got != want {
			t.Fatalf("ParseSurfaceRef(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestClientEnsureWorkspaceReusesFixedWorkspaceAndCreatesSurface(t *testing.T) {
	runner := &recordingRunner{out: map[string]string{
		joinArgs([]string{"list-workspaces", "--json"}): workspaceList("workspace:ws1", "repo-claude", "/repo"),
		joinArgs([]string{"new-surface", "--type", "terminal", "--workspace", "workspace:ws1", "--working-directory", "/repo", "--focus", "true"}): "OK surface:sf1 pane:41 workspace:ws1",
	}}
	client := &Client{Runner: runner.run}

	workspace, reused, err := client.EnsureWorkspace(context.Background(), EnsureWorkspaceOpts{
		Cwd:   "/repo",
		Name:  "repo-claude",
		Focus: true,
	})
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if !reused {
		t.Fatal("expected existing workspace to be reused")
	}
	if workspace.String() != "workspace:ws1" {
		t.Fatalf("workspace ref = %q, want workspace:ws1", workspace.String())
	}

	ref, err := client.NewSurface(context.Background(), NewSurfaceOpts{
		WorkspaceRef: workspace.String(),
		Cwd:          "/repo",
		Focus:        true,
	})
	if err != nil {
		t.Fatalf("NewSurface() error = %v", err)
	}
	if ref.SurfaceID != "surface:sf1" {
		t.Fatalf("surface ref = %q, want surface:sf1", ref.SurfaceID)
	}

	if err := client.SendSurface(context.Background(), workspace.String(), ref.SurfaceID, "hello"); err != nil {
		t.Fatalf("SendSurface() error = %v", err)
	}
	if err := client.SendKeySurface(context.Background(), workspace.String(), ref.SurfaceID, "Enter"); err != nil {
		t.Fatalf("SendKeySurface() error = %v", err)
	}

	screen, err := client.ReadScreen(context.Background(), ReadScreenOpts{
		WorkspaceRef: workspace.String(),
		SurfaceRef:   ref.SurfaceID,
		Lines:        120,
	})
	if err != nil {
		t.Fatalf("ReadScreen() error = %v", err)
	}
	if screen != "ok" {
		t.Fatalf("screen = %q, want ok", screen)
	}

	if len(runner.calls) != 5 {
		t.Fatalf("calls = %d, want 5", len(runner.calls))
	}
	wantSurface := []string{"new-surface", "--type", "terminal", "--workspace", "workspace:ws1", "--working-directory", "/repo", "--focus", "true"}
	if !reflect.DeepEqual(runner.calls[1].args, wantSurface) {
		t.Fatalf("new-surface args = %#v, want %#v", runner.calls[1].args, wantSurface)
	}
	wantSend := []string{"send", "--workspace", "workspace:ws1", "--surface", "surface:sf1", "--", "hello"}
	if !reflect.DeepEqual(runner.calls[2].args, wantSend) {
		t.Fatalf("send args = %#v, want %#v", runner.calls[2].args, wantSend)
	}
	wantEnter := []string{"send-key", "--workspace", "workspace:ws1", "--surface", "surface:sf1", "Enter"}
	if !reflect.DeepEqual(runner.calls[3].args, wantEnter) {
		t.Fatalf("send-key args = %#v, want %#v", runner.calls[3].args, wantEnter)
	}
	wantReadScreen := []string{"read-screen", "--workspace", "workspace:ws1", "--surface", "surface:sf1", "--lines", "120"}
	if !reflect.DeepEqual(runner.calls[4].args, wantReadScreen) {
		t.Fatalf("read-screen args = %#v, want %#v", runner.calls[4].args, wantReadScreen)
	}
}

func TestClientEnsureWorkspaceCreatesMissingWorkspace(t *testing.T) {
	runner := &recordingRunner{out: map[string]string{
		joinArgs([]string{"list-workspaces", "--json"}): "cmux: notice\n{\"workspaces\":[]}",
		joinArgs([]string{"new-workspace", "--cwd", "/repo", "--name", "repo-claude", "--focus", "true", "--description", "desc", "--id-format", "both"}): "workspace:ws2",
	}}
	client := &Client{Runner: runner.run}

	workspace, reused, err := client.EnsureWorkspace(context.Background(), EnsureWorkspaceOpts{
		Cwd:         "/repo",
		Name:        "repo-claude",
		Description: "desc",
		Focus:       true,
	})
	if err != nil {
		t.Fatalf("EnsureWorkspace() error = %v", err)
	}
	if reused {
		t.Fatal("expected workspace creation")
	}
	if workspace.String() != "workspace:ws2" {
		t.Fatalf("workspace ref = %q, want workspace:ws2", workspace.String())
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(runner.calls))
	}
}

func TestFocusSessionSelectsMatchingWorkspace(t *testing.T) {
	runner := &recordingRunner{out: map[string]string{
		joinArgs([]string{"list-workspaces", "--json"}): workspaceList("workspace:ws9", "repo-claude", "/repo"),
	}}
	client := &Client{Runner: runner.run}

	if err := FocusSession(context.Background(), client, "/repo", "claude"); err != nil {
		t.Fatalf("FocusSession() error = %v", err)
	}
	// The workspace name encodes the agent (repo-claude), so the matched ref is
	// switched to with select-workspace as the final command.
	want := []string{"select-workspace", "--workspace", "workspace:ws9"}
	last := runner.calls[len(runner.calls)-1].args
	if !reflect.DeepEqual(last, want) {
		t.Fatalf("select-workspace args = %#v, want %#v", last, want)
	}
}

func TestFocusSessionErrorsWhenWorkspaceMissing(t *testing.T) {
	runner := &recordingRunner{out: map[string]string{
		joinArgs([]string{"list-workspaces", "--json"}): `{"workspaces":[]}`,
	}}
	client := &Client{Runner: runner.run}

	err := FocusSession(context.Background(), client, "/repo", "claude")
	if err == nil {
		t.Fatal("FocusSession() error = nil, want error for a missing workspace")
	}
	if !strings.Contains(err.Error(), "no cmux workspace") {
		t.Fatalf("error = %v, want 'no cmux workspace'", err)
	}
}

func joinArgs(args []string) string {
	return strings.Join(args, "\x00")
}

func workspaceList(ref, title, cwd string) string {
	return `{"workspaces":[{"ref":"` + ref + `","title":"` + title + `","custom_title":"` + title + `","current_directory":"` + cwd + `"}]}`
}
