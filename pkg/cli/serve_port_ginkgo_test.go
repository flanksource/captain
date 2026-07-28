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
		options := ServeOptions{Host: "localhost", Port: 0, Dev: true, UIPort: 0, ThreadsFile: "threads.json"}
		Expect(options.validate()).To(Succeed())

		options.Dev = false
		Expect(options.validate()).To(MatchError("invalid --port 0"))
	})

	It("defaults the development UI port to automatic selection", func() {
		flag := NewServeCommand("test").Flags().Lookup("ui-port")
		Expect(flag).NotTo(BeNil())
		Expect(flag.DefValue).To(Equal("0"))
	})

	It("builds Vite arguments with an available port and Vite-owned browser opening", func() {
		args, err := viteDevServerArgs(0, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(args).To(HaveLen(7))
		Expect(args[:3]).To(Equal([]string{"exec", "vite", "--port"}))
		port, err := strconv.Atoi(args[3])
		Expect(err).NotTo(HaveOccurred())
		Expect(port).To(BeNumerically(">", 0))
		Expect(args[4:]).To(Equal([]string{"--open", "--host", "localhost"}))
		Expect(args).NotTo(ContainElement("--strictPort"))
	})

	It("keeps an explicit Vite port strict", func() {
		args, err := viteDevServerArgs(62183, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(args).To(Equal([]string{"exec", "vite", "--port", "62183", "--strictPort", "--host", "localhost"}))
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
