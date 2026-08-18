package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/agentcreds"
)

// materializerFixture builds a materializer over a fake mount and home.
func materializerFixture(t *testing.T) *credentialMaterializer {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "mount")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	return &credentialMaterializer{source: source, home: filepath.Join(root, "home")}
}

// publish writes a credential into the mount the way kubelet does: mode 0400
// and replaced wholesale rather than edited in place, so an update cannot be
// blocked by the previous file's read-only mode.
func publish(t *testing.T, m *credentialMaterializer, key, payload string) {
	t.Helper()
	path := filepath.Join(m.source, key)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(payload), 0o400); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeCopiesEachCredentialToItsCLIPath(t *testing.T) {
	m := materializerFixture(t)
	publish(t, m, agentcreds.ClaudeFilename, `{"claudeAiOauth":{"accessToken":"a"}}`)
	publish(t, m, agentcreds.CodexFilename, `{"auth_mode":"chatgpt"}`)

	if err := m.materialize(); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		filepath.Join(m.home, ".claude", ".credentials.json"): `{"claudeAiOauth":{"accessToken":"a"}}`,
		filepath.Join(m.home, ".codex", "auth.json"):          `{"auth_mode":"chatgpt"}`,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		// The mount is 0400; the copy must be writable by its owner so the CLI
		// can rewrite its own credential, but readable by nobody else.
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", path, info.Mode().Perm())
		}
	}
}

func TestMaterializeIsQuietWhenAProviderIsNotPublished(t *testing.T) {
	// The supervisor publishes only its configured providers, so a missing key
	// is normal rather than an error.
	m := materializerFixture(t)
	publish(t, m, agentcreds.ClaudeFilename, `{"claudeAiOauth":{"accessToken":"a"}}`)

	if err := m.materialize(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.home, ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Errorf("codex credential was materialized from nothing: %v", err)
	}
}

func TestMaterializePicksUpARepublishedCredential(t *testing.T) {
	m := materializerFixture(t)
	publish(t, m, agentcreds.ClaudeFilename, `{"claudeAiOauth":{"accessToken":"first"}}`)
	if err := m.materialize(); err != nil {
		t.Fatal(err)
	}

	publish(t, m, agentcreds.ClaudeFilename, `{"claudeAiOauth":{"accessToken":"second"}}`)
	if err := m.materialize(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(m.home, ".claude", ".credentials.json")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"claudeAiOauth":{"accessToken":"second"}}` {
		t.Errorf("republished credential was not picked up: %q", got)
	}
}

func TestMaterializeLeavesAnUnchangedCredentialAlone(t *testing.T) {
	// Rewriting every tick would hand the CLIs a new mtime every 30s.
	m := materializerFixture(t)
	publish(t, m, agentcreds.ClaudeFilename, `{"claudeAiOauth":{"accessToken":"a"}}`)
	if err := m.materialize(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(m.home, ".claude", ".credentials.json")
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.materialize(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("an unchanged credential was rewritten")
	}
}

func TestMaterializeLeavesNoTempFilesBehind(t *testing.T) {
	m := materializerFixture(t)
	publish(t, m, agentcreds.ClaudeFilename, `{"claudeAiOauth":{"accessToken":"a"}}`)
	if err := m.materialize(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(m.home, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != ".credentials.json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("unexpected contents: %v", names)
	}
}

func TestMountedReportsAbsenceWithoutFailing(t *testing.T) {
	// A sidecar deployed without credentials is the normal case.
	m := materializerFixture(t)
	if !m.mounted() {
		t.Error("an existing mount directory reported as absent")
	}

	m.source = filepath.Join(t.TempDir(), "not-there")
	if m.mounted() {
		t.Error("a missing mount reported as present")
	}
}

func TestMountedRejectsAFileMasqueradingAsTheMount(t *testing.T) {
	m := materializerFixture(t)
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.source = path
	if m.mounted() {
		t.Error("a plain file was accepted as the credential mount")
	}
}
