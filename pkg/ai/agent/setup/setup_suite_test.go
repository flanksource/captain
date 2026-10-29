package setup_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The developer's global git config (excludes such as .env, signing, hooks)
// must not change what the specs' repos report, so every git call sees only
// the repo-local config the specs write.
func TestSetupSuite(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	RegisterFailHandler(Fail)
	RunSpecs(t, "setup")
}
