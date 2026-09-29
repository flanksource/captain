package sessiontree_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSessionHierarchy(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Session Hierarchy")
}
