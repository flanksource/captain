package cmux

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CmuxBin", func() {
	var gui, cli, bin string

	BeforeEach(func() {
		root, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		gui = filepath.Join(root, "cmux.app", "Contents", "MacOS", "cmux")
		cli = filepath.Join(root, "cmux.app", "Contents", "Resources", "bin", "cmux")
		bin = filepath.Join(root, "bin")
		Expect(os.MkdirAll(bin, 0o755)).To(Succeed())
		for _, path := range []string{gui, cli} {
			Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
			Expect(os.WriteFile(path, nil, 0o755)).To(Succeed())
		}
		GinkgoT().Setenv("CMUX_BIN", "")
	})

	It("resolves the bundled CLI when PATH contains the GUI executable", func() {
		GinkgoT().Setenv("PATH", filepath.Dir(gui))
		Expect(CmuxBin()).To(Equal(cli))
	})

	It("resolves the bundled CLI through a PATH symlink to the GUI", func() {
		Expect(os.Symlink(gui, filepath.Join(bin, "cmux"))).To(Succeed())
		GinkgoT().Setenv("PATH", bin)
		Expect(CmuxBin()).To(Equal(cli))
	})

	It("resolves a GUI override to its bundled CLI", func() {
		GinkgoT().Setenv("CMUX_BIN", gui)
		Expect(CmuxBin()).To(Equal(cli))
	})

	It("preserves an explicit CLI override ahead of PATH", func() {
		GinkgoT().Setenv("CMUX_BIN", cli)
		GinkgoT().Setenv("PATH", filepath.Dir(gui))
		Expect(CmuxBin()).To(Equal(cli))
	})

	It("preserves a standalone CLI from PATH", func() {
		path := filepath.Join(bin, "cmux")
		Expect(os.WriteFile(path, nil, 0o755)).To(Succeed())
		GinkgoT().Setenv("PATH", bin)
		Expect(CmuxBin()).To(Equal(path))
	})

	It("rejects a missing bundled CLI instead of returning the GUI executable", func() {
		Expect(os.Remove(cli)).To(Succeed())
		GinkgoT().Setenv("PATH", filepath.Dir(gui))
		path, err := CmuxBin()
		Expect(path).To(BeEmpty())
		Expect(err).To(MatchError(ContainSubstring("resolve bundled cmux CLI")))
	})

	It("rejects a non-executable bundled CLI", func() {
		Expect(os.Chmod(cli, 0o644)).To(Succeed())
		GinkgoT().Setenv("PATH", filepath.Dir(gui))
		_, err := CmuxBin()
		Expect(err).To(MatchError(ContainSubstring("resolve bundled cmux CLI")))
	})

	It("rejects a missing explicit override", func() {
		GinkgoT().Setenv("CMUX_BIN", filepath.Join(bin, "missing"))
		_, err := CmuxBin()
		Expect(err).To(MatchError(ContainSubstring("resolve cmux CLI")))
	})
})
