package promptcontext_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPromptContext(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "promptcontext")
}
