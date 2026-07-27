package database

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDatabaseOptions(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Database Options Suite")
}
