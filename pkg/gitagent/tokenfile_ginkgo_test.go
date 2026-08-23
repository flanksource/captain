package gitagent_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

var _ = Describe("token files", func() {
	It("rejects a path containing parent traversal", func() {
		root := GinkgoT().TempDir()
		path := filepath.Join(root, "nested") + string(filepath.Separator) + ".." +
			string(filepath.Separator) + "token"

		err := gitagent.WriteTokenFile(path, text.NewSensitiveString("cptn_id.secret"))

		Expect(err).To(MatchError(ContainSubstring("must not contain '..'")))
		_, statErr := os.Stat(filepath.Join(root, "token"))
		Expect(statErr).To(MatchError(os.ErrNotExist))
	})
})
