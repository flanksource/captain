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

func (o ServeOptions) validate() error {
	if strings.TrimSpace(o.Host) == "" {
		return fmt.Errorf("host cannot be empty")
	}
	if o.Port < 0 || o.Port > 65535 || (!o.Dev && o.Port == 0) {
		return fmt.Errorf("invalid --port %d", o.Port)
	}
	if o.Dev && (o.UIPort < 1 || o.UIPort > 65535) {
		return fmt.Errorf("invalid --ui-port %d", o.UIPort)
	}
	if strings.TrimSpace(o.ThreadsFile) == "" {
		return fmt.Errorf("threads file cannot be empty")
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
