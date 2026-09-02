package runtimeprofiles

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func openRuntimeDB(ctx context.Context, name string) *database.DB {
	GinkgoHelper()
	handle := dbtest.ForGinkgo(dbtest.Options{Name: name})
	db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(db.Close)
	return db
}

func sameDB(db *database.DB) func(context.Context) (*database.DB, error) {
	return func(context.Context) (*database.DB, error) { return db, nil }
}

var _ = Describe("Database source", func() {
	It("requires a reader and is read-only without a writer", func(ctx SpecContext) {
		_, err := NewDBSource(DBSourceOptions{})
		Expect(err).To(MatchError(ContainSubstring("Read opener")))

		db := openRuntimeDB(ctx, "captain_runtimeprofiles_readonly")
		source, err := NewDBSource(DBSourceOptions{Read: sameDB(db)})
		Expect(err).NotTo(HaveOccurred())
		Expect(source.Info()).To(Equal(SourceInfo{
			Kind: SourceDB, ID: "db", Label: "Database", Writable: false, Records: []Kind{KindPreset, KindProfile},
		}))
		_, err = source.Presets().Create(ctx, PresetInput{Name: "Nope", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrReadOnly))
		Expect(source.Profiles().Delete(ctx, uuid.New().String())).To(MatchError(ErrReadOnly))
		listed, err := source.Presets().List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(BeEmpty())
	})

	It("stores presets and profiles with encoded ids and mapped errors", func(ctx SpecContext) {
		db := openRuntimeDB(ctx, "captain_runtimeprofiles_store")
		source, err := NewDBSource(DBSourceOptions{Read: sameDB(db), Write: sameDB(db)})
		Expect(err).NotTo(HaveOccurred())
		Expect(source.Info().Writable).To(BeTrue())

		created, err := source.Presets().Create(ctx, PresetInput{
			Name: "Personal", Description: "mine", Scope: api.SpecLayerUser, Spec: presetSpecFixture(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(uuid.Parse(created.Key)).Error().NotTo(HaveOccurred(), "the key is the row id")
		Expect(created.ID).To(Equal(EncodeID(KindPreset, DBSourceID, created.Key)))
		Expect(created.Source).To(Equal(source.Info()))
		Expect(created.Spec).To(Equal(presetSpecFixture()))
		Expect(created.UpdatedAt).NotTo(BeZero())

		Expect(source.Presets().Get(ctx, created.Key)).To(Equal(created))
		_, err = source.Presets().Get(ctx, "not-a-uuid")
		Expect(err).To(MatchError(ErrNotFound))
		_, err = source.Presets().Get(ctx, uuid.New().String())
		Expect(err).To(MatchError(ErrNotFound))
		Expect(err).To(MatchError(database.ErrRuntimePresetNotFound), "the store's sentinel stays in the chain")

		_, err = source.Presets().Create(ctx, PresetInput{Name: "personal", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrNameTaken))
		_, err = source.Presets().Create(ctx, PresetInput{Name: "Bad", Scope: "team"})
		Expect(err).To(MatchError(ErrInvalid))

		updated, err := source.Presets().Update(ctx, created.Key, PresetInput{Name: "Renamed", Scope: api.SpecLayerContext})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.ID).To(Equal(created.ID))
		Expect(updated.Name).To(Equal("Renamed"))
		Expect(updated.UpdatedAt).To(BeTemporally(">", created.UpdatedAt))
		_, err = source.Presets().Update(ctx, uuid.New().String(), PresetInput{Name: "Ghost", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrNotFound))

		profile, err := source.Profiles().Create(ctx, ProfileInput{
			Name: "Review", Presets: []string{created.ID, "Other"},
			Spec: api.Spec{Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(profile.ID).To(Equal(EncodeID(KindProfile, DBSourceID, profile.Key)))
		Expect(profile.Presets).To(Equal([]string{created.ID, "Other"}))
		Expect(source.Profiles().Get(ctx, profile.Key)).To(Equal(profile))
		listed, err := source.Profiles().List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(Equal([]Profile{profile}))

		Expect(source.Profiles().Delete(ctx, profile.Key)).To(Succeed())
		Expect(source.Profiles().Delete(ctx, profile.Key)).To(MatchError(ErrNotFound))
		Expect(source.Presets().Delete(ctx, created.Key)).To(Succeed())
		Expect(source.Presets().Delete(ctx, created.Key)).To(MatchError(ErrNotFound))
	})

	It("serves as the catalog's default target and resolves across a file source", func(ctx SpecContext) {
		db := openRuntimeDB(ctx, "captain_runtimeprofiles_catalog")
		dbSource, err := NewDBSource(DBSourceOptions{Read: sameDB(db), Write: sameDB(db)})
		Expect(err).NotTo(HaveOccurred())
		files := presetDir(GinkgoT().TempDir(), false)
		catalog, err := NewCatalog(dbSource, files)
		Expect(err).NotTo(HaveOccurred())

		org, err := catalog.CreatePreset(ctx, "", PresetInput{
			Name: "Organization", Scope: api.SpecLayerGlobal,
			Spec: api.RuntimePresetSpec{Model: api.Model{Name: "gpt-5", Mode: api.ModeAgent}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(org.Source.Kind).To(Equal(SourceDB))
		personal, err := catalog.CreatePreset(ctx, files.Info().ID, PresetInput{Name: "Personal", Scope: api.SpecLayerUser})
		Expect(err).NotTo(HaveOccurred())
		_, err = catalog.CreatePreset(ctx, files.Info().ID, PresetInput{Name: "organization", Scope: api.SpecLayerUser})
		Expect(err).To(MatchError(ErrNameTaken))

		review, err := catalog.CreateProfile(ctx, "", ProfileInput{Name: "Review", Presets: []string{"personal", "Organization"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(review.Source.Kind).To(Equal(SourceDB))

		resolution, err := catalog.Resolve(ctx, review.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Profile.Presets).To(Equal([]string{personal.ID, org.ID}))
		Expect(resolution.Resolved.Spec.Model.Name).To(Equal("gpt-5"))

		err = catalog.DeletePreset(ctx, "personal")
		var referenced ReferencedError
		Expect(err).To(BeAssignableToTypeOf(referenced))
		Expect(err.Error()).To(ContainSubstring("Review"))
	})
})
