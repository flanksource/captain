package exectool_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestExecTool(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Inline exec tools")
}
