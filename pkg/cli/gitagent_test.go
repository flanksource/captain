package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/commons-db/dbtest"
)

func isolatedConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".captain.yaml")
	captainconfig.SetPathForTesting(path)
	t.Cleanup(func() { captainconfig.SetPathForTesting("") })
	return path
}

// The hand-off `git-agent add` prints is what an operator copies onto the agent
// host, so every part of it has to be there and the credential has to work more
// than once.
func TestGitAgentAddMintsADurableToken(t *testing.T) {
	path := isolatedConfig(t)
	db := gitAgentTokenDB(t)

	res, err := RunGitAgentAdd(t.Context(), GitAgentAddOptions{Name: "worker-1", Backend: "git-agent"})
	if err != nil {
		t.Fatal(err)
	}
	add, ok := res.(GitAgentAddResult)
	if !ok {
		t.Fatalf("result = %T", res)
	}
	if add.HostFingerprint == "" || !strings.Contains(add.JoinCommand, "--token ") {
		t.Fatalf("join hand-off incomplete: %+v", add)
	}
	if !strings.Contains(add.JoinCommand, "--host-fingerprint "+add.HostFingerprint) {
		t.Fatalf("join command must pin the host key: %s", add.JoinCommand)
	}
	if add.Expires != nil {
		t.Fatalf("a token minted with no --expires should not expire, got %s", add.Expires)
	}

	// The credential never lands in the config file: it lives in the database,
	// hashed, and the config holds only dispatch targeting data (R8.2).
	token := strings.Fields(strings.SplitAfter(add.JoinCommand, "--token ")[1])[0]
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("the raw token must not be persisted to the config file")
	}
	if strings.Contains(string(raw), "pending") {
		t.Fatal("pending enrollments are gone: a durable token needs no redemption record")
	}

	// Admitted repeatedly, because a restarting sidecar presents the same one.
	dir := gitAgentDirectory{backend: "git-agent", ctx: t.Context(), db: db}
	for attempt := 1; attempt <= 3; attempt++ {
		name, err := dir.AdmitToken(token, "")
		if err != nil || name != "worker-1" {
			t.Fatalf("admission %d = %q, %v", attempt, name, err)
		}
	}

	if _, err := dir.AdmitToken("cptn_nosuch.secret", ""); err == nil ||
		!strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("an unissued token must be refused, got %v", err)
	}
}

// A revoked token stops working on the very next enrollment, and says so
// rather than reading as an unknown credential.
func TestGitAgentRevokedTokenIsRefusedWithItsReason(t *testing.T) {
	isolatedConfig(t)
	db := gitAgentTokenDB(t)

	res, err := RunGitAgentAdd(t.Context(), GitAgentAddOptions{Name: "worker-1", Backend: "git-agent"})
	if err != nil {
		t.Fatal(err)
	}
	add := res.(GitAgentAddResult)
	dir := gitAgentDirectory{backend: "git-agent", ctx: t.Context(), db: db}
	if _, err := dir.AdmitToken(add.Token.Value(), ""); err != nil {
		t.Fatal(err)
	}

	if err := db.RevokeAPIToken(t.Context(), add.TokenID, "agent decommissioned"); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.AdmitToken(add.Token.Value(), ""); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("a revoked token must be refused as revoked, got %v", err)
	}
}

// A pool token names its members as they arrive and caps how many there can be,
// so one credential can serve a scaled deployment.
func TestGitAgentPoolTokenNamesItsMembers(t *testing.T) {
	isolatedConfig(t)
	db := gitAgentTokenDB(t)

	res, err := RunGitAgentAdd(t.Context(), GitAgentAddOptions{
		Name: "prod-pool", Backend: "git-agent", Pool: true, MaxAgents: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	add := res.(GitAgentAddResult)
	if !add.Pool {
		t.Fatal("a --pool mint must report itself as a pool")
	}
	dir := gitAgentDirectory{backend: "git-agent", ctx: t.Context(), db: db}

	first, err := dir.AdmitToken(add.Token.Value(), "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := dir.AdmitToken(add.Token.Value(), "")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two members must get distinct names, both got %q", first)
	}

	// A restart re-presents the name the member persisted, and must not consume
	// a third slot — otherwise a rescheduled pod exhausts the pool.
	if again, err := dir.AdmitToken(add.Token.Value(), first); err != nil || again != first {
		t.Fatalf("returning member = %q, %v; want %q", again, err, first)
	}
	if _, err := dir.AdmitToken(add.Token.Value(), ""); err == nil || !strings.Contains(err.Error(), "members") {
		t.Fatalf("a full pool must refuse a new member, got %v", err)
	}
}

// gitAgentTokenDB points the default database context at an embedded postgres,
// which is where tokens live.
func gitAgentTokenDB(t *testing.T) *database.DB {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_gitagent_tokens"})
	db, err := database.Open(t.Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	setCaptainDBForTest(db)
	t.Cleanup(func() {
		setCaptainDBForTest(nil)
		resetCaptainContextsForTest()
		_ = db.Close()
	})
	return db
}

func TestGitAgentEnrollListRevoke(t *testing.T) {
	isolatedConfig(t)
	dir := gitAgentDirectory{backend: "git-agent"}
	if err := dir.RecordAgent(gitagent.AgentEnrollment{
		Name: "worker-1", Fingerprint: "SHA256:abc",
		URL: "ssh://127.0.0.1:7422/repo.git", HostFingerprint: "SHA256:host",
	}); err != nil {
		t.Fatal(err)
	}
	if name, ok := dir.AgentByFingerprint("SHA256:abc"); !ok || name != "worker-1" {
		t.Fatalf("lookup = %q, %v", name, ok)
	}

	res, err := RunGitAgentList(GitAgentListOptions{Backend: "git-agent"})
	if err != nil {
		t.Fatal(err)
	}
	entries := res.([]GitAgentListEntry)
	if len(entries) != 1 || entries[0].Name != "worker-1" || entries[0].Status != "enrolled" {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Name: "worker-1", Backend: "git-agent", DryRun: true}); err != nil {
		t.Fatalf("dry-run existing agent: %v", err)
	}

	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Name: "worker-1", Backend: "git-agent"}); err != nil {
		t.Fatal(err)
	}
	// Revocation is effective for lookups after it (R8.5).
	if _, ok := dir.AgentByFingerprint("SHA256:abc"); ok {
		t.Fatal("revoked fingerprint must be refused")
	}
	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Name: "worker-1", Backend: "git-agent"}); err == nil {
		t.Fatal("revoking an unknown agent must error")
	}
	if _, err := RunGitAgentRevoke(GitAgentRevokeOptions{Name: "worker-1", Backend: "git-agent", DryRun: true}); err == nil {
		t.Fatal("dry-run revoking an unknown agent must error")
	}
}

func TestGitAgentAddDryRunTouchesNothing(t *testing.T) {
	path := isolatedConfig(t)
	// No database is wired: a dry run must reach no store either, or it is not
	// dry.
	result, err := RunGitAgentAdd(t.Context(), GitAgentAddOptions{Name: "worker-1", Backend: "git-agent", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if add, ok := result.(GitAgentAddResult); !ok || !add.DryRun {
		t.Fatalf("dry-run result = %#v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not write the config, stat err = %v", err)
	}
	keysDir := filepath.Join(filepath.Dir(path), ".captain", "sandbox")
	if _, err := os.Stat(keysDir); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create key material, stat err = %v", err)
	}
}

func TestEnsureGitAgentBackendRejectsDifferentKind(t *testing.T) {
	cfg := captainconfig.Config{}
	cfg.Sandbox.Backends = map[string]captainconfig.SandboxBackend{
		"shared": {Kind: "srt"},
	}
	if _, err := ensureGitAgentBackend(&cfg, "shared"); err == nil || !strings.Contains(err.Error(), "not git-agent") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeWorkflowRejectsUnknownFields(t *testing.T) {
	_, err := decodeWorkflow(map[string]any{"verify": map[string]any{"promtps": []string{"judge.prompt"}}})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}
