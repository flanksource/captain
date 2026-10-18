package cmux

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCmuxSpecs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "cmux Suite")
}
