package cli

import (
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CLI runtime catalog discovery", func() {
	var configPath string

	BeforeEach(func() {
		home := GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		GinkgoT().Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		GinkgoT().Chdir(GinkgoT().TempDir())
		configPath = filepath.Join(home, ".captain.yaml")
		captainconfig.SetPathForTesting(configPath)
		DeferCleanup(captainconfig.SetPathForTesting, "")
	})

	It("preserves the supplied catalog without reading user config", func() {
		fixture := newRuntimeEntityFixture()
		Expect(os.WriteFile(configPath, []byte("runtime: [malformed\n"), 0o600)).To(Succeed())
		Expect(buildRuntimeCatalog(fixture.ctx, runtimeprofiles.DefaultCatalogOptions{})).To(BeIdenticalTo(fixture.catalog))
	})

	It("uses the shared discovery sources with the CLI database registered first", func(ctx SpecContext) {
		expected, err := runtimeprofiles.NewDefaultCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{
			Read: captainDB, Write: captainDefaultDB,
		})
		Expect(err).NotTo(HaveOccurred())
		actual, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(actual.Sources()).To(Equal(expected.Sources()))
		Expect(actual.Sources()).To(HaveLen(5))
		Expect(actual.Sources()[0].Kind).To(Equal(runtimeprofiles.SourceDB))
	})

	It("reports invalid configured discovery directories", func(ctx SpecContext) {
		Expect(os.WriteFile(configPath, []byte("runtime:\n  profileDirs: [missing]\n"), 0o600)).To(Succeed())
		_, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
		Expect(err).To(MatchError(ContainSubstring("runtime.profileDirs")))
		Expect(err).To(MatchError(ContainSubstring("missing")))
	})
})
