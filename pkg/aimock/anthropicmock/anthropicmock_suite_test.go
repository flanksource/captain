package anthropicmock

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAnthropicMock(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Anthropic Mock Suite")
}
