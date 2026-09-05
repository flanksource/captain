package runtimeprofiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/database"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Default runtime catalog", func() {
	var home, cwd, configHome string

	BeforeEach(func() {
		home = GinkgoT().TempDir()
		cwd = GinkgoT().TempDir()
		configHome = filepath.Join(home, "xdg", "captain")
		GinkgoT().Setenv("HOME", home)
		GinkgoT().Setenv("XDG_CONFIG_HOME", filepath.Dir(configHome))
		captainconfig.SetPathForTesting(filepath.Join(home, ".captain.yaml"))
		DeferCleanup(captainconfig.SetPathForTesting, "")
	})

	It("discovers user and explicit working-directory records without opening a database", func(ctx SpecContext) {
		writeRecordFile(filepath.Join(configHome, "presets"), "personal.yaml", "name: Personal\nscope: user\n")
		writeRecordFile(filepath.Join(cwd, ".captain", "profiles"), "review.yaml", "name: Review\npresets: [Personal]\n")
		catalog, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalog.Sources()).To(Equal([]SourceInfo{
			defaultFileInfo(KindPreset, filepath.Join(configHome, "presets"), true),
			defaultFileInfo(KindProfile, filepath.Join(configHome, "profiles"), true),
			defaultFileInfo(KindPreset, filepath.Join(cwd, ".captain", "presets"), true),
			defaultFileInfo(KindProfile, filepath.Join(cwd, ".captain", "profiles"), true),
		}))
		resolution, err := catalog.Resolve(ctx, "Review")
		Expect(err).NotTo(HaveOccurred())
		Expect(resolution.Profile.Source.Root).To(Equal(filepath.Join(cwd, ".captain", "profiles")))
		Expect(resolution.Presets).To(HaveLen(1))
		Expect(resolution.Presets[0].Name).To(Equal("Personal"))
	})

	It("uses the process directory and standard user config directory when omitted", func(ctx SpecContext) {
		GinkgoT().Setenv("XDG_CONFIG_HOME", "")
		GinkgoT().Chdir(cwd)
		actualCwd, err := os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		catalog, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalog.Sources()).To(Equal([]SourceInfo{
			defaultFileInfo(KindPreset, filepath.Join(home, ".config", "captain", "presets"), true),
			defaultFileInfo(KindProfile, filepath.Join(home, ".config", "captain", "profiles"), true),
			defaultFileInfo(KindPreset, filepath.Join(actualCwd, ".captain", "presets"), true),
			defaultFileInfo(KindProfile, filepath.Join(actualCwd, ".captain", "profiles"), true),
		}))
	})

	It("loads configured directories relative to the config file and expands home paths", func(ctx SpecContext) {
		configDir := filepath.Join(home, "settings")
		captainconfig.SetPathForTesting(filepath.Join(configDir, "captain.yaml"))
		writeRecordFile(configDir, "captain.yaml", "runtime:\n  presetDirs: [presets]\n  profileDirs: [~/profiles]\n")
		writeRecordFile(filepath.Join(configDir, "presets"), "shared.yaml", "name: Shared\nscope: global\n")
		writeRecordFile(filepath.Join(home, "profiles"), "review.yaml", "name: Review\npresets: [Shared]\n")
		presets, err := filepath.EvalSymlinks(filepath.Join(configDir, "presets"))
		Expect(err).NotTo(HaveOccurred())
		profiles, err := filepath.EvalSymlinks(filepath.Join(home, "profiles"))
		Expect(err).NotTo(HaveOccurred())
		catalog, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalog.Sources()[2:4]).To(Equal([]SourceInfo{
			defaultFileInfo(KindPreset, presets, false),
			defaultFileInfo(KindProfile, profiles, false),
		}))
		Expect(catalog.Resolve(ctx, "Review")).Error().NotTo(HaveOccurred())
	})

	It("uses supplied config and deduplicates configured directory aliases", func(ctx SpecContext) {
		writeRecordFile(home, ".captain.yaml", "runtime: [malformed\n")
		presets := filepath.Join(home, "presets")
		writeRecordFile(presets, "shared.yaml", "name: Shared\nscope: global\n")
		alias := filepath.Join(home, "alias")
		Expect(os.Symlink(presets, alias)).To(Succeed())
		cfg := captainconfig.Config{Runtime: captainconfig.RuntimeDefaults{PresetDirs: []string{presets, alias}}}
		catalog, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd, Config: &cfg})
		Expect(err).NotTo(HaveOccurred())
		Expect(catalog.Sources()).To(HaveLen(5))
		Expect(catalog.ListPresets(ctx)).To(HaveLen(1))
	})

	It("registers the database first and defers opener failures until records are read", func(ctx SpecContext) {
		openerErr := errors.New("database unavailable")
		calls := 0
		opener := func(context.Context) (*database.DB, error) {
			calls++
			return nil, openerErr
		}
		catalog, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd, Read: opener, Write: opener})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(BeZero())
		Expect(catalog.Sources()[0]).To(Equal(SourceInfo{
			Kind: SourceDB, ID: DBSourceID, Label: "Database", Writable: true, Records: []Kind{KindPreset, KindProfile},
		}))
		_, err = catalog.ListProfiles(ctx)
		Expect(err).To(MatchError(openerErr))
		Expect(calls).To(Equal(1))
	})

	It("rejects a write opener without a read opener", func(ctx SpecContext) {
		_, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd, Write: sameDB(nil)})
		Expect(err).To(MatchError(ContainSubstring("Read opener")))
	})

	It("reports malformed loaded configuration", func(ctx SpecContext) {
		writeRecordFile(home, ".captain.yaml", "runtime: [malformed\n")
		_, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd})
		Expect(err).To(MatchError(ContainSubstring("parse " + filepath.Join(home, ".captain.yaml"))))
	})

	DescribeTable("rejects invalid configured directories",
		func(ctx SpecContext, raw, message string) {
			cfg := captainconfig.Config{Runtime: captainconfig.RuntimeDefaults{ProfileDirs: []string{raw}}}
			_, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd, Config: &cfg})
			Expect(err).To(MatchError(ContainSubstring("runtime.profileDirs")))
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("empty", "  ", "cannot be empty"),
		Entry("missing", "missing", "no such file or directory"),
		Entry("unsupported home alias", "~someone/profiles", "unsupported home-relative"),
	)

	It("rejects a configured path that is a file", func(ctx SpecContext) {
		writeRecordFile(home, "profiles", "not a directory")
		cfg := captainconfig.Config{Runtime: captainconfig.RuntimeDefaults{ProfileDirs: []string{"profiles"}}}
		_, err := NewDefaultCatalog(ctx, DefaultCatalogOptions{Cwd: cwd, Config: &cfg})
		Expect(err).To(MatchError(ContainSubstring("is not a directory")))
	})

	It("refuses canceled catalog construction", func(ctx SpecContext) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := NewDefaultCatalog(canceled, DefaultCatalogOptions{Cwd: cwd})
		Expect(err).To(MatchError(context.Canceled))
	})
})

func defaultFileInfo(kind Kind, dir string, implicit bool) SourceInfo {
	return SourceInfo{
		Kind: SourceFile, ID: hashDir(dir), Label: dir, Root: dir, Writable: true,
		Implicit: implicit, Records: []Kind{kind},
	}
}
