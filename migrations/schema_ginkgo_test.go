package migrations

import (
	"io/fs"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// addedCheckConstraint extracts the CHECK body of the script's
// ADD CONSTRAINT ... statement, with whitespace collapsed so the two files are
// compared on what they say rather than how they are indented.
func addedCheckConstraint(script, name string) string {
	GinkgoHelper()
	_, after, found := strings.Cut(script, "ADD CONSTRAINT captain_turn_requests_tool_approval_identity")
	Expect(found).To(BeTrue(), "%s no longer adds the constraint", name)
	body, _, found := strings.Cut(after, ") NOT VALID;")
	Expect(found).To(BeTrue(), "%s no longer ends the constraint with ) NOT VALID;", name)
	return strings.Join(strings.Fields(body), " ")
}

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

		var sqlFiles []string
		Expect(fs.WalkDir(filesystem, ".", func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && strings.HasSuffix(name, ".sql") {
				sqlFiles = append(sqlFiles, name)
			}
			return nil
		})).To(Succeed())
		Expect(sqlFiles).NotTo(BeEmpty())
		for _, name := range sqlFiles {
			content, err := fs.ReadFile(filesystem, name)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).NotTo(ContainSubstring(DefaultSchema+".captain_"), name)
		}

		stateTriggers, err := fs.ReadFile(filesystem, "51_state_triggers.sql")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(stateTriggers)).To(ContainSubstring(schemaName + ".captain_sessions"))

		hclContent, err := fs.ReadFile(filesystem, "10_sessions.pg.hcl")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(hclContent)).To(ContainSubstring("schema.public"))
		Expect(string(hclContent)).NotTo(ContainSubstring(schemaName))
	})

	It("rejects invalid schemas", func() {
		_, err := schemaFilesystem(strings.Repeat("x", 64))
		Expect(err).To(HaveOccurred())
	})

	// 74 is retired but still installs the tool-approval identity constraint,
	// because either script can re-run without the other (a content-hash change,
	// a dropped ledger row) and whichever runs last decides the shape the
	// database ends up with. They only agree by staying byte-identical.
	It("installs one tool-approval identity constraint from both 74 and 81", func() {
		bodies := map[string]string{}
		for _, name := range []string{
			"74_turn_request_approval_identity.sql",
			"81_turn_request_provider_approval_identity.sql",
		} {
			content, err := schemaFS.ReadFile(name)
			Expect(err).NotTo(HaveOccurred())
			bodies[name] = addedCheckConstraint(string(content), name)
		}
		Expect(bodies["74_turn_request_approval_identity.sql"]).
			To(Equal(bodies["81_turn_request_provider_approval_identity.sql"]))
		for name, body := range bodies {
			Expect(body).To(ContainSubstring("credential_id IS NULL"), name)
		}
	})

	It("uses a stable schema-specific advisory lock", func() {
		Expect(migrationLockKey(DefaultSchema)).To(Equal(captainMigrationLockKey))
		Expect(migrationLockKey("agent_namespace_one")).To(Equal(migrationLockKey("agent_namespace_one")))
		Expect(migrationLockKey("agent_namespace_one")).NotTo(Equal(migrationLockKey("agent_namespace_two")))
	})
})
