package ai_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAttachmentCapabilities(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AI Attachment Capabilities Suite")
}
