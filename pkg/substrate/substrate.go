// Package substrate drives agent-substrate environments: it creates one from an image with its
// egress policy, moves files in and out, runs its processes, and deletes it.
package substrate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/substrate/internal/ateapipb"
	"github.com/flanksource/captain/pkg/substrate/internal/ateenvv1alpha"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Options reach one substrate: its ate-env-api and ate-api-server, directly or through an ingress
// that requires a client certificate.
type Options struct {
	// EnvAPI is the ate-env-api gRPC address, such as ate-env-api.ate-env.svc:7777.
	EnvAPI string
	// Control is the ate-api-server gRPC address, which egress policies are set through.
	Control string
	// ControlTokenFile holds the bearer token the control plane accepts, such as a projected
	// ServiceAccount token with the api.ate-system.svc audience. It is read on every call.
	ControlTokenFile string
	// CAFile holds the CA substrate's certificates chain to. Without it or a client certificate,
	// the control plane's certificate isn't verified, as a port-forwarded control plane needs.
	CAFile string
	// ClientCertFile and ClientKeyFile hold the client certificate the ingress in front of a
	// substrate in another cluster requires. With them, both APIs are reached over TLS verified
	// against the system roots, or CAFile. They are read on every handshake, so a renewed
	// certificate is picked up without a restart.
	ClientCertFile string
	ClientKeyFile  string
	// Atespace is the atespace environments are created in; empty is ate-env.
	Atespace string
	// Template is the ActorTemplate environments derive from, its container replaced by the
	// environment's image; empty is env's default-template.
	Template string
	// TrustBundle is the path, inside an environment, of the egress gateway's CA, which a process
	// must trust to reach a host whose requests carry an injected credential.
	TrustBundle string
}

// Client is a connection to one substrate.
type Client struct {
	options   Options
	conns     []*grpc.ClientConn
	envs      ateenvv1alpha.EnvironmentServiceClient
	processes ateenvv1alpha.ProcessServiceClient
	files     ateenvv1alpha.FileSystemServiceClient
	control   ateapipb.ControlClient
}

// Dial connects to the substrate options name. Connections are made on first use.
func Dial(options Options) (*Client, error) {
	if options.EnvAPI == "" || options.Control == "" {
		return nil, errors.New("substrate needs the ate-env-api and ate-api-server addresses")
	}
	if options.Atespace == "" {
		options.Atespace = "ate-env"
	}
	if (options.ClientCertFile == "") != (options.ClientKeyFile == "") {
		return nil, errors.New("the substrate client certificate needs both its certificate and key files")
	}
	remote := options.ClientCertFile != ""
	tlsConfig := &tls.Config{InsecureSkipVerify: options.CAFile == "" && !remote} //nolint:gosec // a port-forwarded control plane serves a cluster-internal certificate
	if options.CAFile != "" {
		pem, err := os.ReadFile(options.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read substrate's CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("substrate's CA file %s holds no certificate", options.CAFile)
		}
		tlsConfig.RootCAs = pool
	}
	// ate-env-api serves cleartext gRPC inside the cluster; from another cluster it is behind the
	// ingress's TLS.
	envCredentials := insecure.NewCredentials()
	if remote {
		if _, err := clientCertificate(options); err != nil {
			return nil, err
		}
		tlsConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return clientCertificate(options)
		}
		envCredentials = credentials.NewTLS(tlsConfig)
	}
	envConn, err := grpc.NewClient(options.EnvAPI, grpc.WithTransportCredentials(envCredentials))
	if err != nil {
		return nil, fmt.Errorf("connect to ate-env-api %s: %w", options.EnvAPI, err)
	}
	dial := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))}
	if options.ControlTokenFile != "" {
		dial = append(dial, grpc.WithPerRPCCredentials(tokenFile(options.ControlTokenFile)))
	}
	controlConn, err := grpc.NewClient(options.Control, dial...)
	if err != nil {
		_ = envConn.Close()
		return nil, fmt.Errorf("connect to ate-api-server %s: %w", options.Control, err)
	}
	return &Client{
		options:   options,
		conns:     []*grpc.ClientConn{envConn, controlConn},
		envs:      ateenvv1alpha.NewEnvironmentServiceClient(envConn),
		processes: ateenvv1alpha.NewProcessServiceClient(envConn),
		files:     ateenvv1alpha.NewFileSystemServiceClient(envConn),
		control:   ateapipb.NewControlClient(controlConn),
	}, nil
}

// Close closes the client's connections.
func (c *Client) Close() error {
	var errs []error
	for _, conn := range c.conns {
		errs = append(errs, conn.Close())
	}
	return errors.Join(errs...)
}

// Environment is an environment to create: its ID in the client's atespace, the image its
// container runs, pinned by digest, and what it may reach.
type Environment struct {
	ID     string
	Image  string
	Egress Egress
}

// Env is a created environment.
type Env struct {
	client      *Client
	id          string
	credentials []Credential
}

// Create creates environment and sets its actor's egress policy. An environment whose policy
// can't be set is deleted, since without it the environment reaches nothing it needs.
func (c *Client) Create(ctx context.Context, environment Environment) (*Env, error) {
	if err := PinnedImage(environment.Image); err != nil {
		return nil, err
	}
	policy, err := egressPolicy(c.options.Atespace, environment.Egress)
	if err != nil {
		return nil, err
	}
	create := &ateenvv1alpha.CreateEnvironmentRequest{Id: environment.ID, Atespace: c.options.Atespace, RootfsImage: environment.Image}
	if c.options.Template != "" {
		create.Template = &ateenvv1alpha.Template{Name: c.options.Template, Atespace: c.options.Atespace}
	}
	env := &Env{client: c, id: environment.ID, credentials: environment.Egress.Credentials}
	if _, err := c.envs.CreateEnvironment(ctx, create); err != nil {
		return nil, fmt.Errorf("create substrate environment %s: %w", env.name(), err)
	}
	if _, err := c.control.CreateActorEgressPolicy(ctx, &ateapipb.CreateActorEgressPolicyRequest{
		Actor: &ateapipb.ObjectRef{Atespace: c.options.Atespace, Name: environment.ID}, EgressPolicy: policy,
	}); err != nil {
		return nil, errors.Join(
			fmt.Errorf("set the egress policy of substrate environment %s: %w", env.name(), err),
			env.Delete(context.WithoutCancel(ctx)),
		)
	}
	return env, nil
}

// PinnedImage refuses an image that isn't pinned by digest, which Substrate requires.
func PinnedImage(image string) error {
	if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(image) {
		return fmt.Errorf("substrate runs images pinned by digest: %q must be repo@sha256:<digest>", image)
	}
	return nil
}

// ID is the environment's ID in its atespace.
func (e *Env) ID() string { return e.id }

// Actor is the environment's SPIFFE ID, which the egress gateway names it by.
func (e *Env) Actor() string { return Actor(e.client.options.Atespace, e.id) }

// Actor is the SPIFFE ID of the environment id in atespace.
func Actor(atespace, id string) string {
	return "spiffe://substrate-actor.local/actor/" + atespace + "/" + id
}

func (e *Env) name() string { return e.client.options.Atespace + "/" + e.id }

// guest addresses ctx's calls to the environment's guest, as ate-env-api routes them.
func (e *Env) guest(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-env-id", e.id, "x-env-atespace", e.client.options.Atespace)
}

// Delete deletes the environment and everything running in it.
func (e *Env) Delete(ctx context.Context) error {
	if _, err := e.client.envs.DeleteEnvironment(ctx, &ateenvv1alpha.DeleteEnvironmentRequest{Id: e.id, Atespace: e.client.options.Atespace}); err != nil {
		return fmt.Errorf("delete substrate environment %s: %w", e.name(), err)
	}
	return nil
}

// fileChunk is how much of a file one message carries.
const fileChunk = 256 * 1024

// WriteFile writes what r holds to path in the environment, with mode, creating its directories.
func (e *Env) WriteFile(ctx context.Context, path string, mode os.FileMode, r io.Reader) error {
	stream, err := e.client.files.WriteFile(e.guest(ctx))
	if err != nil {
		return fmt.Errorf("write %s to substrate environment %s: %w", path, e.name(), err)
	}
	first := &ateenvv1alpha.WriteFileRequest{Path: path, Mode: uint32(mode.Perm())}
	buf := make([]byte, fileChunk)
	for {
		n, readErr := r.Read(buf)
		if n > 0 || first != nil {
			message := first
			if message == nil {
				message = &ateenvv1alpha.WriteFileRequest{}
			}
			message.Chunk = buf[:n]
			first = nil
			if err := stream.Send(message); err != nil {
				_, err = stream.CloseAndRecv()
				return fmt.Errorf("write %s to substrate environment %s: %w", path, e.name(), err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = stream.CloseSend()
			return fmt.Errorf("read what to write to %s in substrate environment %s: %w", path, e.name(), readErr)
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("write %s to substrate environment %s: %w", path, e.name(), err)
	}
	return nil
}

// ReadFile reads path in the environment. A missing file is a NOT_FOUND error, from the call or
// from the first read.
func (e *Env) ReadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := e.client.files.ReadFile(e.guest(ctx), &ateenvv1alpha.ReadFileRequest{Path: path})
	if err != nil {
		cancel()
		return nil, e.readError(path, err)
	}
	return &fileReader{stream: stream, cancel: cancel, failed: func(err error) error { return e.readError(path, err) }}, nil
}

func (e *Env) readError(path string, err error) error {
	return fmt.Errorf("read %s from substrate environment %s: %w", path, e.name(), err)
}

type fileReader struct {
	stream  grpc.ServerStreamingClient[ateenvv1alpha.ReadFileResponse]
	cancel  context.CancelFunc
	failed  func(error) error
	pending []byte
}

func (f *fileReader) Read(p []byte) (int, error) {
	for len(f.pending) == 0 {
		message, err := f.stream.Recv()
		if err == io.EOF {
			return 0, io.EOF
		}
		if err != nil {
			return 0, f.failed(err)
		}
		f.pending = message.GetChunk()
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func (f *fileReader) Close() error {
	f.cancel()
	return nil
}

// Placeholder is what a process holds in place of a credential the egress gateway injects: its
// client sends it in the header the gateway replaces with the real credential.
const Placeholder = "injected-by-the-substrate-egress-gateway"

// Process is a process to start in an environment.
type Process struct {
	Command []string
	// Cwd is the working directory inside the environment; empty is the guest's workspace.
	Cwd string
	// Env is set on top of the guest's environment, along with a Placeholder in each injected
	// credential's variable. Setting such a variable here is refused: the real credential would
	// enter the environment.
	Env map[string]string
	// Timeout kills the process when it runs longer; zero is the guest's default.
	Timeout time.Duration
	// Stdin keeps the process's stdin open for Proc.Input; without it, stdin reads as empty.
	Stdin bool
}

// Proc is a started process.
type Proc struct {
	env *Env
	id  string
}

// Start starts process in the environment.
func (e *Env) Start(ctx context.Context, process Process) (*Proc, error) {
	env := map[string]string{}
	for key, value := range process.Env {
		env[key] = value
	}
	for _, credential := range e.credentials {
		if credential.Env == "" {
			continue
		}
		if _, set := env[credential.Env]; set {
			return nil, fmt.Errorf("%s would enter substrate environment %s; the egress gateway injects %s's credential, so the process gets a placeholder", credential.Env, e.name(), credential.Host)
		}
		env[credential.Env] = Placeholder
	}
	if e.client.options.TrustBundle != "" && len(e.credentials) > 0 {
		env["NODE_EXTRA_CA_CERTS"] = e.client.options.TrustBundle
	}
	request := &ateenvv1alpha.StartProcessRequest{Command: process.Command, Cwd: process.Cwd, Env: env, Stdin: process.Stdin}
	if process.Timeout > 0 {
		request.Timeout = durationpb.New(process.Timeout)
	}
	started, err := e.client.processes.StartProcess(e.guest(ctx), request)
	if err != nil {
		return nil, fmt.Errorf("start %s in substrate environment %s: %w", strings.Join(process.Command, " "), e.name(), err)
	}
	return &Proc{env: e, id: started.GetProcessId()}, nil
}

// ID is the process's ID in its environment.
func (p *Proc) ID() string { return p.id }

// Output follows the process's stdout and stderr, interleaved, until it exits.
func (p *Proc) Output(ctx context.Context) io.ReadCloser { return p.follow(ctx, true, true) }

// Stdout follows the process's stdout until it exits.
func (p *Proc) Stdout(ctx context.Context) io.ReadCloser { return p.follow(ctx, true, false) }

// Stderr follows the process's stderr until it exits.
func (p *Proc) Stderr(ctx context.Context) io.ReadCloser { return p.follow(ctx, false, true) }

// skipped is an offset past the end of any spool, which suppresses that stream.
const skipped = math.MaxInt64

// follow follows the process's stdout, stderr or both until it exits. Substrate's router ends a
// long stream, so it reconnects from the offsets it has read.
func (p *Proc) follow(ctx context.Context, withStdout, withStderr bool) io.ReadCloser {
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	go func() {
		defer cancel()
		var stdout, stderr int64
		if !withStdout {
			stdout = skipped
		}
		if !withStderr {
			stderr = skipped
		}
		for {
			stream, err := p.env.client.processes.StreamProcessOutput(p.env.guest(ctx), &ateenvv1alpha.StreamProcessOutputRequest{
				ProcessId: p.id, Follow: true, StdoutOffset: stdout, StderrOffset: stderr,
			})
			if err != nil {
				writer.CloseWithError(fmt.Errorf("follow the output of %s in substrate environment %s: %w", p.id, p.env.name(), err))
				return
			}
			for {
				message, err := stream.Recv()
				if err != nil {
					break
				}
				var chunk []byte
				switch {
				case message.GetExit() != nil:
					writer.Close()
					return
				case message.GetStdout() != nil:
					chunk = message.GetStdout()
					stdout += int64(len(chunk))
				default:
					chunk = message.GetStderr()
					stderr += int64(len(chunk))
				}
				if _, err := writer.Write(chunk); err != nil {
					return
				}
			}
			if ctx.Err() != nil {
				writer.CloseWithError(ctx.Err())
				return
			}
		}
	}()
	return &output{PipeReader: reader, cancel: cancel}
}

type output struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (o *output) Close() error {
	o.cancel()
	return o.PipeReader.Close()
}

// Input writes to the stdin of a process started with Stdin. Each write is its own call, so a
// stream Substrate's router ends never loses input; Close closes stdin.
func (p *Proc) Input(ctx context.Context) io.WriteCloser { return input{proc: p, ctx: ctx} }

type input struct {
	proc *Proc
	ctx  context.Context
}

func (in input) Write(data []byte) (int, error) {
	if err := in.send(&ateenvv1alpha.WriteProcessInputRequest{ProcessId: in.proc.id, Data: data}); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (in input) Close() error {
	return in.send(&ateenvv1alpha.WriteProcessInputRequest{ProcessId: in.proc.id, Close: true})
}

func (in input) send(request *ateenvv1alpha.WriteProcessInputRequest) error {
	p := in.proc
	stream, err := p.env.client.processes.WriteProcessInput(p.env.guest(in.ctx))
	if err == nil {
		err = stream.Send(request)
	}
	if err == nil || err == io.EOF {
		_, err = stream.CloseAndRecv()
	}
	if err != nil {
		return fmt.Errorf("write to the stdin of %s in substrate environment %s: %w", p.id, p.env.name(), err)
	}
	return nil
}

// waitInterval is how often Wait asks whether the process exited; a single stream doesn't
// outlast Substrate's router.
const waitInterval = 2 * time.Second

// Wait returns the process's exit code once it exits: its own, or 128 plus the signal that
// ended it.
func (p *Proc) Wait(ctx context.Context) (int, error) {
	for {
		process, err := p.env.client.processes.GetProcess(p.env.guest(ctx), &ateenvv1alpha.GetProcessRequest{ProcessId: p.id})
		if err != nil && ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err == nil && process.GetState() == ateenvv1alpha.ProcessState_PROCESS_STATE_EXITED {
			return int(process.GetExitCode()), nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(waitInterval):
		}
	}
}

// Kill kills the process and its process group. A process that already exited needs no killing.
func (p *Proc) Kill(ctx context.Context) error {
	_, err := p.env.client.processes.SignalProcess(p.env.guest(ctx), &ateenvv1alpha.SignalProcessRequest{ProcessId: p.id, Signal: ateenvv1alpha.Signal_SIGNAL_KILL})
	if err != nil && !exited(err) {
		return fmt.Errorf("kill %s in substrate environment %s: %w", p.id, p.env.name(), err)
	}
	return nil
}

// exited reports whether err refuses a signal because the process already exited.
func exited(err error) bool {
	return status.Code(err) == codes.FailedPrecondition && strings.Contains(status.Convert(err).Message(), "has exited")
}

// clientCertificate loads the client certificate a substrate in another cluster requires.
func clientCertificate(options Options) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(options.ClientCertFile, options.ClientKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load the substrate client certificate %s: %w", options.ClientCertFile, err)
	}
	return &cert, nil
}

// tokenFile sends the bearer token in a file, read on every call so a rotated token is used.
type tokenFile string

func (t tokenFile) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	token, err := os.ReadFile(string(t))
	if err != nil {
		return nil, fmt.Errorf("read the control plane token: %w", err)
	}
	return map[string]string{"authorization": "Bearer " + strings.TrimSpace(string(token))}, nil
}

func (tokenFile) RequireTransportSecurity() bool { return true }
