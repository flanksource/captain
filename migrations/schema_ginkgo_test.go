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

// activityFunction extracts the captain_touch_session_activity definition.
func activityFunction(script, name string) string {
	GinkgoHelper()
	_, after, found := strings.Cut(script, "CREATE OR REPLACE FUNCTION public.captain_touch_session_activity()")
	Expect(found).To(BeTrue(), "%s no longer defines captain_touch_session_activity", name)
	body, _, found := strings.Cut(after, "\n$$;")
	Expect(found).To(BeTrue(), "%s no longer closes the function with $$;", name)
	return body
}

// activityNotifyBlock is the comment and IF block 83 inserts ahead of the
// activity allowlist.
func activityNotifyBlock(body string) string {
	GinkgoHelper()
	start := strings.Index(body, "  -- Above the activity guard")
	Expect(start).To(BeNumerically(">=", 0), "83 no longer explains the notify placement")
	length := strings.Index(body[start:], "  END IF;\n")
	Expect(length).To(BeNumerically(">=", 0))
	return body[start : start+length+len("  END IF;\n")]
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

	// 83 redefines 52's activity function to add the change notification. Both
	// scripts can re-run (83 depends on 52, so an edit to 52 replays 83), and
	// they only agree on everything except the notify by staying identical.
	It("redefines the session activity function with only the change notification added", func() {
		bodies := map[string]string{}
		for _, name := range []string{"52_session_activity_triggers.sql", "83_session_change_notify.sql"} {
			content, err := schemaFS.ReadFile(name)
			Expect(err).NotTo(HaveOccurred())
			bodies[name] = activityFunction(string(content), name)
		}
		notify := activityNotifyBlock(bodies["83_session_change_notify.sql"])
		Expect(notify).To(ContainSubstring("PERFORM pg_notify('captain_session_change', session_id_value::text);"))
		withoutNotify := strings.Replace(bodies["83_session_change_notify.sql"], notify, "", 1)
		Expect(strings.Join(strings.Fields(withoutNotify), " ")).
			To(Equal(strings.Join(strings.Fields(bodies["52_session_activity_triggers.sql"]), " ")))

		content, err := schemaFS.ReadFile("83_session_change_notify.sql")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("-- dependsOn: 52_session_activity_triggers.sql"))
	})

	It("uses a stable schema-specific advisory lock", func() {
		Expect(migrationLockKey(DefaultSchema)).To(Equal(captainMigrationLockKey))
		Expect(migrationLockKey("agent_namespace_one")).To(Equal(migrationLockKey("agent_namespace_one")))
		Expect(migrationLockKey("agent_namespace_one")).NotTo(Equal(migrationLockKey("agent_namespace_two")))
	})
})
