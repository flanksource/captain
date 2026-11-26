package runowner_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRunOwner(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "runowner")
}
