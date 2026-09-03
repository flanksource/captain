package agent

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAgentSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "pkg/ai/agent")
}
