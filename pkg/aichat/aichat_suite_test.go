package aichat_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAIChat(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AI Chat Transport Suite")
}
