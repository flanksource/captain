package gitagent_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestGitAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GitAgent Suite")
}
