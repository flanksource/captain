// The service tunnel: a byte stream from a task's agent back to a loopback
// service the supervisor run publishes for that task — its caller-tool MCP
// server, a credential proxy, a one-shot credential release. It rides the
// mailbox transport the sidecar already relays results over, so it opens no
// new network path and trusts no new identity.
//
// The client never names an address. The mailbox serve process splices only to
// an address the supervisor run registered for the task, only on loopback, and
// only for the agent the task was dispatched to. The registry exists from the
// moment of dispatch until the supervisor has its final outcome, which is the
// whole lifetime a service is needed for.
package gitagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
)

// TunnelCommand is the SSH exec verb and HTTPS endpoint that open a tunnel.
const TunnelCommand = "captain-tunnel"

const servicesFile = "services.json"

// Services maps a service name to the loopback host:port serving it.
type Services map[string]string

var serviceNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*(/[A-Za-z0-9][A-Za-z0-9_-]*)?$`)

// ValidServiceName accepts "tools", "egress/<tool>" and the like: a kind and
// an optional tool name, never a path or an address.
func ValidServiceName(name string) bool { return serviceNameRe.MatchString(name) }

// Validate refuses a registry entry that is not a loopback address: the serve
// process dials what is registered, and a non-loopback entry would turn the
// tunnel into a way to reach anything the supervisor host can.
func (s Services) Validate() error {
	for _, name := range s.names() {
		if !ValidServiceName(name) {
			return fmt.Errorf("service name %q is not a valid service name", name)
		}
		if err := loopbackAddr(s[name]); err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
	}
	return nil
}

func (s Services) names() []string {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func loopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return fmt.Errorf("address %q is not host:port", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("address %q is not a loopback IP", addr)
	}
	return nil
}

func servicesPath(repo, task string) string {
	return filepath.Join(taskStateDir(repo, task), servicesFile)
}

// WriteServices registers a task's services, readable only by this user.
func WriteServices(repo, task string, services Services) error {
	if err := ValidateTaskID(task); err != nil {
		return err
	}
	if err := services.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(services)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(taskStateDir(repo, task), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(servicesPath(repo, task), data, 0o600)
}

// RemoveServices withdraws a task's services; a tunnel opened afterwards is
// refused. Removing a registry that is already gone is not an error.
func RemoveServices(repo, task string) error {
	if err := ValidateTaskID(task); err != nil {
		return err
	}
	if err := os.Remove(servicesPath(repo, task)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func readServices(repo, task string) (Services, error) {
	data, err := os.ReadFile(servicesPath(repo, task))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("task %s publishes no services", task)
	}
	if err != nil {
		return nil, err
	}
	var services Services
	if err := json.Unmarshal(data, &services); err != nil {
		return nil, fmt.Errorf("task %s services: %w", task, err)
	}
	return services, services.Validate()
}

// OpenService connects an authenticated agent to one of its task's services.
// Every open is logged, granted or refused, because the tunnel reaches the
// supervisor's own loopback.
func OpenService(repo, agent, task, service string) (net.Conn, error) {
	conn, err := openService(repo, agent, task, service)
	if err != nil {
		logger.Warnf("git-agent tunnel refused: agent=%s task=%s service=%s: %v", agent, task, service, err)
		return nil, err
	}
	logger.Infof("git-agent tunnel opened: agent=%s task=%s service=%s", agent, task, service)
	return conn, nil
}

func openService(repo, agent, task, service string) (net.Conn, error) {
	if err := ValidateTaskID(task); err != nil {
		return nil, err
	}
	if !ValidServiceName(service) {
		return nil, fmt.Errorf("service name %q is not a valid service name", service)
	}
	state, ok, err := LoadTaskState(repo, task)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("task %s is unknown", task)
	}
	if agent == "" || state.Agent != agent {
		return nil, fmt.Errorf("task %s was not dispatched to agent %q", task, agent)
	}
	services, err := readServices(repo, task)
	if err != nil {
		return nil, err
	}
	addr, ok := services[service]
	if !ok {
		return nil, fmt.Errorf("task %s publishes no service %q", task, service)
	}
	return net.DialTimeout("tcp", addr, 10*time.Second)
}

// Splice copies both ways between a tunnel's two ends until either closes,
// then closes both.
func Splice(tunnel io.ReadWriteCloser, service net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(service, tunnel)
		closeWrite(service)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(tunnel, service)
		_ = tunnel.Close()
	}()
	wg.Wait()
	_ = service.Close()
}

func closeWrite(conn net.Conn) {
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
		return
	}
	_ = conn.Close()
}

// ServiceDialer opens a connection to one of a task's services.
type ServiceDialer func(ctx context.Context, service string) (net.Conn, error)

// ServeForwarder listens on a fresh loopback port and relays every connection
// it accepts to service through dial, so a local client — an MCP client, an
// HTTP proxy client, kubectl — reaches the supervisor's service as if it were
// local. It returns the listening address and a closer.
func ServeForwarder(ctx context.Context, dial ServiceDialer, service string) (string, func() error, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("forwarder for %s: %w", service, err)
	}
	go func() {
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				remote, err := dial(ctx, service)
				if err != nil {
					logger.Warnf("git-agent tunnel to %s: %v", service, err)
					_ = local.Close()
					return
				}
				Splice(remote, local)
			}()
		}
	}()
	return listener.Addr().String(), listener.Close, nil
}
