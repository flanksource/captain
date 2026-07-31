package database

import (
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("project session aggregates", func() {
	It("reads only root session picker fields and falls back to an active process cwd", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_project_store"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		earlier := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
		latest := earlier.Add(time.Hour)
		first, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: uuid.New(), ProviderSessionID: uuid.NewString(), Source: "codex", HostID: "test-host", CWD: "/repo/pkg/cli",
		})
		Expect(err).NotTo(HaveOccurred())
		second, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: uuid.New(), ProviderSessionID: uuid.NewString(), Source: "codex", HostID: "test-host", CWD: "/repo/pkg/cli",
		})
		Expect(err).NotTo(HaveOccurred())
		fallback, err := db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: uuid.New(), ProviderSessionID: uuid.NewString(), Source: "claude", HostID: "test-host",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = db.CreateOrGetSession(ctx, CreateSessionInput{
			ID: uuid.New(), ProviderSessionID: uuid.NewString(), Source: "codex", HostID: "test-host", CWD: "/repo/child", ParentSessionID: &first.ID,
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(db.gorm.WithContext(ctx).Table("captain_sessions").Where("id = ?", first.ID).Update("last_activity_at", earlier).Error).NotTo(HaveOccurred())
		Expect(db.gorm.WithContext(ctx).Table("captain_sessions").Where("id = ?", second.ID).Update("last_activity_at", latest).Error).NotTo(HaveOccurred())
		Expect(db.gorm.WithContext(ctx).Table("captain_sessions").Where("id = ?", fallback.ID).Update("cwd", "").Error).NotTo(HaveOccurred())
		Expect(db.UpsertSessionProcess(ctx, SessionProcessInput{
			SessionID: fallback.ID, HostID: "test-host", BootID: "boot", PID: 123, ProcessStartedAt: earlier,
			Status: "running", Command: "codex", CWD: "/repo/pkg/database", Source: "codex", SampledAt: latest,
		})).To(Succeed())

		rows, err := db.ListProjectSessionAggregates(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(ConsistOf(
			MatchFields(IgnoreExtras, Fields{
				"CWD":            Equal("/repo/pkg/cli"),
				"Source":         Equal("codex"),
				"SessionCount":   Equal(2),
				"ProcessActive":  BeFalse(),
				"LastActivityAt": PointTo(BeTemporally("==", latest)),
			}),
			MatchFields(IgnoreExtras, Fields{
				"CWD":            Equal("/repo/pkg/database"),
				"Source":         Equal("claude"),
				"SessionCount":   Equal(1),
				"ProcessActive":  BeTrue(),
				"LastActivityAt": BeNil(),
			}),
		))
	})
})
