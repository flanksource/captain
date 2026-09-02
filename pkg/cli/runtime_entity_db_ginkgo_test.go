package cli

import (
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("runtime entities over the database", func() {
	var f runtimeEntityFixture

	BeforeEach(func() {
		withGinkgoCaptainDB()
		dbSource, err := runtimeprofiles.NewDBSource(runtimeprofiles.DBSourceOptions{Read: captainDB, Write: captainDefaultDB})
		Expect(err).NotTo(HaveOccurred())
		f = newRuntimeEntityFixture(dbSource)
	})

	It("stores a create without a target in the database", func() {
		record := f.createPreset(organizationPresetBody)

		Expect(record.Source.Kind).To(Equal(runtimeprofiles.SourceDB))
		ref, err := runtimeprofiles.DecodeID(record.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(ref).To(Equal(runtimeprofiles.RecordRef{Kind: runtimeprofiles.KindPreset, SourceID: runtimeprofiles.DBSourceID, Key: record.Key}))
		Expect(getRuntimePreset(f.ctx, "organization")).To(Equal(record))
	})

	It("lists database and file records together, database first", func() {
		f.createPreset(withTarget(map[string]any{"name": "Alpha", "scope": "user"}, f.presets.ID))
		f.createPreset(map[string]any{"name": "Zulu", "scope": "user"})

		listed, err := listRuntimePresets(f.ctx, RuntimePresetListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(2))
		Expect(listed[0].Source.Kind).To(Equal(runtimeprofiles.SourceDB))
		Expect(listed[0].Name).To(Equal("Zulu"))
		Expect(listed[1].Source.Kind).To(Equal(runtimeprofiles.SourceFile))
		Expect(listed[1].Name).To(Equal("Alpha"))
		Expect(listRuntimePresets(f.ctx, RuntimePresetListOptions{Source: "db"})).To(HaveLen(1))
	})

	It("resolves a database profile referencing a file preset by name", func() {
		organization := f.createPreset(withTarget(organizationPresetBody, f.presets.ID))
		review := f.createProfile(map[string]any{"name": "Review", "presets": []string{"organization"}})
		Expect(review.Source.Kind).To(Equal(runtimeprofiles.SourceDB))

		resolution, err := resolveRuntimeProfileAction(f.ctx, review.ID, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Profile.Source.Kind).To(Equal(runtimeprofiles.SourceDB))
		Expect(resolution.Profile.Presets).To(Equal([]string{organization.ID}))
		Expect(resolution.Presets).To(Equal([]runtimeprofiles.Preset{organization.Preset}))
		Expect(resolution.Resolved.Trace[0].ID).To(Equal(organization.ID))
		Expect(resolution.Resolved.Spec.Budget.MaxTurns).To(Equal(20))
	})
})
