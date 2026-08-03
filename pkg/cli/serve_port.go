package cli

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

func effectiveServePort(dev, portFlagSet bool, configuredPort int) int {
	if dev && !portFlagSet {
		return 0
	}
	return configuredPort
}

func viteDevServerArgs(configuredPort int, open bool) ([]string, error) {
	port := configuredPort
	if port == 0 {
		listener, _, selectedPort, err := listenCaptainServer("localhost", 0)
		if err != nil {
			return nil, fmt.Errorf("select Vite dev port: %w", err)
		}
		if err := listener.Close(); err != nil {
			return nil, fmt.Errorf("release Vite dev port %d: %w", selectedPort, err)
		}
		port = selectedPort
	}

	args := []string{"exec", "vite", "--port", strconv.Itoa(port)}
	if configuredPort != 0 {
		args = append(args, "--strictPort")
	}
	if open {
		args = append(args, "--open")
	}
	return append(args, "--host", "localhost"), nil
}

func (o ServeOptions) validate() error {
	if strings.TrimSpace(o.Host) == "" {
		return fmt.Errorf("host cannot be empty")
	}
	if o.Port < 0 || o.Port > 65535 || (!o.Dev && o.Port == 0) {
		return fmt.Errorf("invalid --port %d", o.Port)
	}
	if o.Dev && (o.UIPort < 0 || o.UIPort > 65535) {
		return fmt.Errorf("invalid --ui-port %d", o.UIPort)
	}
	return ValidatePromptDirs(o.PromptDirs)
}

func listenCaptainServer(host string, port int) (net.Listener, string, int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, "", 0, fmt.Errorf("listen on %s:%d: %w", host, port, err)
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, "", 0, fmt.Errorf("unexpected listener address %T", listener.Addr())
	}
	return listener, net.JoinHostPort(host, strconv.Itoa(tcpAddr.Port)), tcpAddr.Port, nil
}
