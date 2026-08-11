package migrations

import (
	"io/fs"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("schema-scoped Captain migrations", func() {
	It("leaves the public bundle unchanged", func() {
		filesystem, err := schemaFilesystem(DefaultSchema)
		Expect(err).NotTo(HaveOccurred())
		content, err := fs.ReadFile(filesystem, "51_state_triggers.sql")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("public.captain_sessions"))
	})

	It("qualifies SQL objects with the selected schema while retaining portable HCL", func() {
		const schemaName = "agent_namespace_context"
		filesystem, err := schemaFilesystem(schemaName)
		Expect(err).NotTo(HaveOccurred())

		sqlContent, err := fs.ReadFile(filesystem, "51_state_triggers.sql")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sqlContent)).To(ContainSubstring(schemaName + ".captain_sessions"))
		Expect(string(sqlContent)).NotTo(ContainSubstring(DefaultSchema + ".captain_"))

		hclContent, err := fs.ReadFile(filesystem, "10_sessions.pg.hcl")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(hclContent)).To(ContainSubstring("schema.public"))
		Expect(string(hclContent)).NotTo(ContainSubstring(schemaName))
	})

	It("rejects invalid schemas", func() {
		_, err := schemaFilesystem(strings.Repeat("x", 64))
		Expect(err).To(HaveOccurred())
	})

	It("uses a stable schema-specific advisory lock", func() {
		Expect(migrationLockKey(DefaultSchema)).To(Equal(captainMigrationLockKey))
		Expect(migrationLockKey("agent_namespace_one")).To(Equal(migrationLockKey("agent_namespace_one")))
		Expect(migrationLockKey("agent_namespace_one")).NotTo(Equal(migrationLockKey("agent_namespace_two")))
	})
})
