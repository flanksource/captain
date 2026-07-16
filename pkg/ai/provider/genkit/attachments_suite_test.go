package genkit

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAttachments(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Genkit Attachments Suite")
}
