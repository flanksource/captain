package runtimeprofiles

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRuntimeProfiles(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Runtime Profiles Suite")
}
