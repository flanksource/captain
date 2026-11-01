package deliver

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSessionDeliver(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Session Deliver")
}
