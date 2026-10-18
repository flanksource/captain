package jev

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestJev(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Jev Provider Suite")
}
