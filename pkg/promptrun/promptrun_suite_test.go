package promptrun_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPromptRun(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "promptrun")
}
