package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveMCPPromptRestrictions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.prompt")
	source := `---
permissions:
  mcp:
    servers: [github, filesystem]
    filesystem: disabled
    slack: enabled
  tools:
    mcp__github__get_issue: allow
    mcp__github__delete_issue: deny
    mcp__slack__*: ask
    Bash: deny
---
Review the issue.
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveMCPPromptRestrictions(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "review" {
		t.Fatalf("Name = %q", got.Name)
	}
	if !reflect.DeepEqual(got.Servers, []string{"filesystem", "github", "slack"}) {
		t.Fatalf("Servers = %v", got.Servers)
	}
	if !reflect.DeepEqual(got.DisabledServers, []string{"filesystem"}) {
		t.Fatalf("DisabledServers = %v", got.DisabledServers)
	}
	if !reflect.DeepEqual(got.AllowTools, []string{"mcp__github__get_issue", "mcp__slack__*"}) {
		t.Fatalf("AllowTools = %v", got.AllowTools)
	}
	if !reflect.DeepEqual(got.DenyTools, []string{"mcp__github__delete_issue"}) {
		t.Fatalf("DenyTools = %v", got.DenyTools)
	}
}

func TestResolveMCPPromptRestrictionsRejectsInvalidMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.prompt")
	source := `---
permissions:
  mcp:
    github: disable
---
Review the issue.
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveMCPPromptRestrictions(path); err == nil {
		t.Fatal("expected invalid MCP mode to be rejected")
	}
}
