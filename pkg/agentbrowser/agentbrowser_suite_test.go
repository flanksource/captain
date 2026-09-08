package agentbrowser

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAgentBrowser(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Browser")
}
