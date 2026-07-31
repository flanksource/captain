package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
)

func TestRunSessionLiveReadsStoredProcessStatusWithoutPolling(t *testing.T) {
	home := t.TempDir()
	db := withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	stored, err := db.CreateOrGetSession(t.Context(), database.CreateSessionInput{
		ProviderSessionID: "sess-stored", Source: "codex", CWD: project,
	})
	if err != nil {
		t.Fatalf("create stored session: %v", err)
	}
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	sampledAt := started.Add(30 * time.Second)
	expiresAt := sampledAt.Add(time.Minute)
	if err := db.UpsertSessionProcess(t.Context(), database.SessionProcessInput{
		SessionID: stored.ID, HostID: captainHostID(), BootID: "boot", PID: 24680,
		ProcessStartedAt: started, SampledAt: sampledAt,
		Status: "sleeping", CWD: project, Source: "codex",
	}); err != nil {
		t.Fatalf("upsert stored process: %v", err)
	}
	if err := db.Gorm().WithContext(t.Context()).Exec(`
		UPDATE captain_session_processes
		SET lease_owner = ?, lease_token = ?, lease_expires_at = ?
		WHERE session_id = ?`, "captain-serve", "6522fe00-9a7c-4cee-a205-123456789abc", expiresAt, stored.ID).Error; err != nil {
		t.Fatalf("set stored process lease: %v", err)
	}

	discoveryCalls := 0
	monitorDiscoverProcesses = func() ([]monitor.Process, error) {
		discoveryCalls++
		return nil, nil
	}

	result, err := RunSessionLive(context.Background(), SessionLiveOptions{Source: "all", Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionLive: %v", err)
	}
	if discoveryCalls != 0 {
		t.Fatalf("process discovery calls = %d, want 0", discoveryCalls)
	}
	if result.Total != 1 || len(result.Sessions) != 1 {
		t.Fatalf("live sessions = %+v", result)
	}
	if result.Database.ReadAt.IsZero() {
		t.Fatal("database read time is missing")
	}
	if result.Database.LatestSampledAt == nil || !result.Database.LatestSampledAt.Equal(sampledAt) {
		t.Fatalf("database latest sample = %v, want %v", result.Database.LatestSampledAt, sampledAt)
	}
	if result.Database.LatestHeartbeatAt == nil || !result.Database.LatestHeartbeatAt.Equal(sampledAt) {
		t.Fatalf("database latest heartbeat = %v, want %v", result.Database.LatestHeartbeatAt, sampledAt)
	}
	if result.Database.EarliestLeaseExpiry == nil || !result.Database.EarliestLeaseExpiry.Equal(expiresAt) {
		t.Fatalf("database lease expiry = %v, want %v", result.Database.EarliestLeaseExpiry, expiresAt)
	}
	live := result.Sessions[0].Live
	if live == nil || live.PID != 24680 || live.Status != "sleeping" {
		t.Fatalf("stored process status = %+v", live)
	}
	if live.SampledAt == nil || !live.SampledAt.Equal(sampledAt) {
		t.Fatalf("stored sample time = %v, want %v", live.SampledAt, sampledAt)
	}
	if live.LastHeartbeatAt == nil || !live.LastHeartbeatAt.Equal(sampledAt) {
		t.Fatalf("stored heartbeat = %v, want %v", live.LastHeartbeatAt, sampledAt)
	}
	if live.LeaseOwner != "captain-serve" {
		t.Fatalf("stored lease owner = %q", live.LeaseOwner)
	}
	if live.LeaseExpiresAt == nil || !live.LeaseExpiresAt.Equal(expiresAt) {
		t.Fatalf("stored lease expiry = %v, want %v", live.LeaseExpiresAt, expiresAt)
	}
}

func refreshTestSessionDB(t *testing.T) {
	t.Helper()
	if _, err := freshenSessionDB(t.Context()); err != nil {
		t.Fatalf("refresh test session database: %v", err)
	}
}

func hasHealth(signals []SessionHealthWire, kind, severity string) bool {
	for _, signal := range signals {
		if signal.Kind == kind && signal.Severity == severity {
			return true
		}
	}
	return false
}
