package callertools_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCallerTools(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Caller Tools Suite")
}
