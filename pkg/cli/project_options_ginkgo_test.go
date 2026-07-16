package cli

import (
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/database"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("project options", func() {
	It("groups focused session aggregates by detected project root", func() {
		root := filepath.Join(GinkgoT().TempDir(), "work", "captain")
		cliDir := filepath.Join(root, "pkg", "cli")
		databaseDir := filepath.Join(root, "pkg", "database")
		Expect(os.MkdirAll(cliDir, 0o755)).To(Succeed())
		Expect(os.MkdirAll(databaseDir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644)).To(Succeed())

		earlier := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
		latest := earlier.Add(time.Hour)
		result := projectOptionsFromAggregates([]database.ProjectSessionAggregate{
			{CWD: cliDir, Source: "codex", SessionCount: 2, LastActivityAt: &earlier},
			{CWD: databaseDir, Source: "claude", SessionCount: 3, ProcessActive: true, LastActivityAt: &latest},
		})

		Expect(result).To(Equal(ProjectOptionsResult{
			Total: 1,
			Projects: []ProjectOption{{
				Value:    root,
				Label:    "work/captain",
				Path:     root,
				Sources:  []string{"claude", "codex", "live"},
				Sessions: 5,
				LastUsed: &latest,
			}},
		}))
	})
})
