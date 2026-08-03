package openaimock

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestOpenAIMock(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "OpenAI Mock Suite")
}
