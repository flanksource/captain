package hookaction_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHookAction(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "hookaction")
}
