package approval_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestApproval(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AI Approval Broker Suite")
}
