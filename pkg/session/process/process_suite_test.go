package process

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSessionProcess(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Session Process")
}
