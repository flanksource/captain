// Runs the bash provider's specs: real commands in real directories, their output streamed back
// and their exit codes reported.
package bash

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBash(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Bash Provider Suite")
}
