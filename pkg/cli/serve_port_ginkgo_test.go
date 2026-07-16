package cli

import (
	"net"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("serve ports", func() {
	DescribeTable("selecting the API port",
		func(dev, portFlagSet bool, configuredPort, expectedPort int) {
			Expect(effectiveServePort(dev, portFlagSet, configuredPort)).To(Equal(expectedPort))
		},
		Entry("uses an ephemeral port for development", true, false, 9020, 0),
		Entry("preserves an explicit development port", true, true, 9021, 9021),
		Entry("preserves the default production port", false, false, 9020, 9020),
	)

	It("accepts an ephemeral port only in development", func() {
		options := ServeOptions{Host: "localhost", Port: 0, Dev: true, UIPort: 5183, ThreadsFile: "threads.json"}
		Expect(options.validate()).To(Succeed())

		options.Dev = false
		Expect(options.validate()).To(MatchError("invalid --port 0"))
	})

	It("keeps the ephemeral API port reserved for the server", func() {
		listener, addr, port, err := listenCaptainServer("127.0.0.1", 0)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(listener.Close)

		Expect(port).To(BeNumerically(">", 0))
		Expect(addr).To(Equal(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))))

		second, err := net.Listen("tcp", addr)
		Expect(err).To(HaveOccurred())
		Expect(second).To(BeNil())
	})
})
