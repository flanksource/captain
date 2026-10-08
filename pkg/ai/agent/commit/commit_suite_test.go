package commit

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCommitSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "commit")
}
