package cli

import (
	"net"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("serve ports", func() {
	It("keeps the configured Captain port in development", func() {
		options := ServeOptions{Host: "localhost", Port: 9020, Dev: true, UIPort: 0}
		Expect(options.validate()).To(Succeed())

		flag := NewServeCommand("test").Flags().Lookup("port")
		Expect(flag).NotTo(BeNil())
		Expect(flag.DefValue).To(Equal("9020"))
	})

	It("rejects an ephemeral API port in every mode", func() {
		options := ServeOptions{Host: "localhost", Port: 0, Dev: true, UIPort: 0}
		Expect(options.validate()).To(MatchError("invalid --port 0"))

		options.Dev = false
		Expect(options.validate()).To(MatchError("invalid --port 0"))
	})

	It("defaults the development UI port to automatic selection", func() {
		flag := NewServeCommand("test").Flags().Lookup("ui-port")
		Expect(flag).NotTo(BeNil())
		Expect(flag.DefValue).To(Equal("0"))
	})

	It("builds Vite arguments with an available port", func() {
		args, port, err := viteDevServerArgs(0)
		Expect(err).NotTo(HaveOccurred())
		Expect(args).To(HaveLen(6))
		Expect(args[:3]).To(Equal([]string{"exec", "vite", "--port"}))
		argPort, err := strconv.Atoi(args[3])
		Expect(err).NotTo(HaveOccurred())
		Expect(port).To(BeNumerically(">", 0))
		Expect(argPort).To(Equal(port))
		Expect(args[4:]).To(Equal([]string{"--host", "localhost"}))
		Expect(args).NotTo(ContainElement("--strictPort"))
	})

	It("keeps an explicit Vite port strict", func() {
		args, port, err := viteDevServerArgs(62183)
		Expect(err).NotTo(HaveOccurred())
		Expect(port).To(Equal(62183))
		Expect(args).To(Equal([]string{"exec", "vite", "--port", "62183", "--strictPort", "--host", "localhost"}))
	})

	It("reserves an automatically selected port while choosing it", func() {
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
