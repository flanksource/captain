package assistanttags_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAssistantTags(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Assistant Tags Suite")
}
