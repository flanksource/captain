// Package substratetest serves Substrate's environment and control APIs in-process for tests:
// each environment is a temp directory, and its processes are real processes run in it.
package substratetest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/substrate"
	ateapipb "github.com/flanksource/captain/pkg/substrate/internal/ateapi"
	"github.com/flanksource/captain/pkg/substrate/internal/ateenvv1alpha"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server is an in-process Substrate. ate-env-api is served in cleartext, as inside the cluster,
// and the control plane over TLS with a self-signed certificate, which a client without a CA file
// accepts.
type Server struct {
	// RefusePolicy, when set, is the error CreateActorEgressPolicy answers with.
	RefusePolicy error
	// RefuseDelete, when set, is the error DeleteEnvironment answers with, as Substrate does when
	// it can't upload an environment's snapshot.
	RefuseDelete error
	// StreamLimit, when positive, ends every StreamProcessOutput stream after that many output
	// messages, as Substrate's router ends a long stream, so a client has to reconnect.
	StreamLimit int
	// ListPageSize, when positive, caps every ListActors page, so a client has to page.
	ListPageSize int

	envAddr, controlAddr string
	root                 string

	mu       sync.Mutex
	envs     map[string]*Environment
	policies map[string]*ateapipb.EgressPolicy
	deleted  []string
}

// Environment is one environment the server created.
type Environment struct {
	ID, Atespace, Template string
	// Dir is the directory the environment's files live in and its processes run in.
	Dir string
	// Created is when the environment was created, its actor's create time.
	Created time.Time

	mu        sync.Mutex
	processes map[string]*process
	next      int
}

// Start serves an in-process Substrate until t ends.
func Start(t testing.TB) *Server {
	t.Helper()
	s := &Server{root: t.TempDir(), envs: map[string]*Environment{}, policies: map[string]*ateapipb.EgressPolicy{}}

	envServer := grpc.NewServer()
	ateenvv1alpha.RegisterEnvironmentServiceServer(envServer, environments{s: s})
	ateenvv1alpha.RegisterProcessServiceServer(envServer, processes{s: s})
	ateenvv1alpha.RegisterFileSystemServiceServer(envServer, files{s: s})
	s.envAddr = serve(t, envServer)

	controlServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{selfSigned(t)}, MinVersion: tls.VersionTLS12})))
	ateapipb.RegisterControlServer(controlServer, control{s: s})
	s.controlAddr = serve(t, controlServer)

	t.Cleanup(s.killAll)
	return s
}

// Options reach this server.
func (s *Server) Options() substrate.Options {
	return substrate.Options{EnvAPI: s.envAddr, Control: s.controlAddr}
}

// Environment returns the live environment id in atespace.
func (s *Server) Environment(atespace, id string) (*Environment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	env, ok := s.envs[atespace+"/"+id]
	return env, ok
}

// Backdate makes the live environment id in atespace look created at created, as one a caller
// left behind long ago.
func (s *Server) Backdate(atespace, id string, created time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if env, ok := s.envs[atespace+"/"+id]; ok {
		env.Created = created
	}
}

// Policy returns the egress policy set for the actor id in atespace.
func (s *Server) Policy(atespace, id string) (*ateapipb.EgressPolicy, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy, ok := s.policies[atespace+"/"+id]
	return policy, ok
}

// Deleted lists the environments deleted so far, as atespace/id.
func (s *Server) Deleted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...)
}

func serve(t testing.TB, server *grpc.Server) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the in-process substrate: %v", err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

func (s *Server) killAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, env := range s.envs {
		env.killAll()
	}
}

// guest is the environment ctx's call is routed to, by the metadata ate-env-api routes by.
func (s *Server) guest(ctx context.Context) (*Environment, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	ids, atespaces := md.Get("x-env-id"), md.Get("x-env-atespace")
	if len(ids) != 1 || len(atespaces) != 1 {
		return nil, status.Error(codes.InvalidArgument, "the call names no environment: x-env-id and x-env-atespace are required")
	}
	env, ok := s.Environment(atespaces[0], ids[0])
	if !ok {
		return nil, status.Errorf(codes.NotFound, "environment %s/%s not found", atespaces[0], ids[0])
	}
	return env, nil
}

type environments struct {
	ateenvv1alpha.UnimplementedEnvironmentServiceServer
	s *Server
}

func (e environments) CreateEnvironment(_ context.Context, req *ateenvv1alpha.CreateEnvironmentRequest) (*ateenvv1alpha.CreateEnvironmentResponse, error) {
	s := e.s
	s.mu.Lock()
	defer s.mu.Unlock()
	key := req.GetAtespace() + "/" + req.GetId()
	if _, ok := s.envs[key]; ok {
		return nil, status.Errorf(codes.AlreadyExists, "environment %s already exists", key)
	}
	dir := filepath.Join(s.root, req.GetAtespace(), req.GetId())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.envs[key] = &Environment{
		ID: req.GetId(), Atespace: req.GetAtespace(), Template: req.GetTemplate().GetName(),
		Dir: dir, Created: time.Now(), processes: map[string]*process{},
	}
	return &ateenvv1alpha.CreateEnvironmentResponse{}, nil
}

func (e environments) DeleteEnvironment(_ context.Context, req *ateenvv1alpha.DeleteEnvironmentRequest) (*ateenvv1alpha.DeleteEnvironmentResponse, error) {
	s := e.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RefuseDelete != nil {
		return nil, s.RefuseDelete
	}
	key := req.GetAtespace() + "/" + req.GetId()
	env, ok := s.envs[key]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "environment %s not found", key)
	}
	env.killAll()
	delete(s.envs, key)
	delete(s.policies, key)
	s.deleted = append(s.deleted, key)
	return &ateenvv1alpha.DeleteEnvironmentResponse{}, nil
}

type control struct {
	ateapipb.UnimplementedControlServer
	s *Server
}

func (c control) CreateActorEgressPolicy(_ context.Context, req *ateapipb.CreateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RefusePolicy != nil {
		return nil, s.RefusePolicy
	}
	actor := req.GetActor()
	s.policies[actor.GetAtespace()+"/"+actor.GetName()] = proto.Clone(req.GetEgressPolicy()).(*ateapipb.EgressPolicy)
	return req.GetEgressPolicy(), nil
}

// ListActors lists the actors of the atespace's environments, in name order, a page at a time:
// the page token is the index of the page's first actor.
func (c control) ListActors(_ context.Context, req *ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var envs []*Environment
	for _, env := range s.envs {
		if env.Atespace == req.GetAtespace() {
			envs = append(envs, env)
		}
	}
	slices.SortFunc(envs, func(a, b *Environment) int { return strings.Compare(a.ID, b.ID) })
	start := 0
	if token := req.GetOptions().GetPageToken(); token != "" {
		var err error
		if start, err = strconv.Atoi(token); err != nil || start < 0 || start > len(envs) {
			return nil, status.Errorf(codes.InvalidArgument, "page token %q", token)
		}
	}
	end := len(envs)
	if size := int(req.GetOptions().GetPageSize()); size > 0 && start+size < end {
		end = start + size
	}
	if s.ListPageSize > 0 && start+s.ListPageSize < end {
		end = start + s.ListPageSize
	}
	response := &ateapipb.ListActorsResponse{}
	for _, env := range envs[start:end] {
		response.Actors = append(response.Actors, &ateapipb.Actor{Metadata: &ateapipb.ResourceMetadata{
			Atespace: env.Atespace, Name: env.ID, CreateTime: timestamppb.New(env.Created),
		}})
	}
	if end < len(envs) {
		response.NextPageToken = strconv.Itoa(end)
	}
	return response, nil
}

// process is one process started in an environment, with its output spooled.
type process struct {
	id      string
	command []string
	cmd     *exec.Cmd
	// stdin is the process's stdin, nil unless it was started with stdin.
	stdin io.WriteCloser

	mu       sync.Mutex
	stdout   []byte
	stderr   []byte
	exited   bool
	exitCode int
	started  time.Time
	finished time.Time
}

type spool struct {
	p      *process
	stderr bool
}

func (w spool) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	if w.stderr {
		w.p.stderr = append(w.p.stderr, b...)
	} else {
		w.p.stdout = append(w.p.stdout, b...)
	}
	return len(b), nil
}

func (p *process) resource() *ateenvv1alpha.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	resource := &ateenvv1alpha.Process{ProcessId: p.id, Command: p.command, State: ateenvv1alpha.ProcessState_PROCESS_STATE_RUNNING, StartedAt: timestamppb.New(p.started)}
	if p.cmd.Process != nil {
		resource.Pid = int32(p.cmd.Process.Pid) //nolint:gosec // a PID fits
	}
	if p.exited {
		resource.State = ateenvv1alpha.ProcessState_PROCESS_STATE_EXITED
		resource.ExitCode = int32(p.exitCode) //nolint:gosec // an exit status fits
		resource.FinishedAt = timestamppb.New(p.finished)
	}
	return resource
}

func (p *process) kill(signal syscall.Signal) {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, signal)
	}
}

func (env *Environment) killAll() {
	env.mu.Lock()
	defer env.mu.Unlock()
	for _, p := range env.processes {
		p.kill(syscall.SIGKILL)
	}
}

func (env *Environment) process(id string) (*process, error) {
	env.mu.Lock()
	defer env.mu.Unlock()
	p, ok := env.processes[id]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "process %s not found", id)
	}
	return p, nil
}

// path is where the guest path name lives: under the environment's directory, relative to it
// when it isn't absolute.
func (env *Environment) path(name string) string {
	return filepath.Join(env.Dir, filepath.Clean("/"+name))
}

type processes struct {
	ateenvv1alpha.UnimplementedProcessServiceServer
	s *Server
}

func (ps processes) StartProcess(ctx context.Context, req *ateenvv1alpha.StartProcessRequest) (*ateenvv1alpha.Process, error) {
	env, err := ps.s.guest(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetCommand()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "the process has no command")
	}
	env.mu.Lock()
	env.next++
	id := "process-" + strconv.Itoa(env.next)
	env.mu.Unlock()

	cmd := exec.Command(req.GetCommand()[0], req.GetCommand()[1:]...) //nolint:gosec // the test's own command
	cmd.Dir = env.path(req.GetCwd())
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + env.Dir}
	for key, value := range req.GetEnv() {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p := &process{id: id, command: req.GetCommand(), cmd: cmd, started: time.Now()}
	cmd.Stdout, cmd.Stderr = spool{p: p}, spool{p: p, stderr: true}
	if req.GetStdin() {
		if p.stdin, err = cmd.StdinPipe(); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "start %v: %v", req.GetCommand(), err)
	}
	env.mu.Lock()
	env.processes[id] = p
	env.mu.Unlock()
	if timeout := req.GetTimeout().AsDuration(); timeout > 0 {
		time.AfterFunc(timeout, func() { p.kill(syscall.SIGKILL) })
	}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		defer p.mu.Unlock()
		p.exited, p.finished = true, time.Now()
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit) && exit.Sys().(syscall.WaitStatus).Signaled():
			p.exitCode = 128 + int(exit.Sys().(syscall.WaitStatus).Signal())
		case errors.As(err, &exit):
			p.exitCode = exit.ExitCode()
		case err != nil:
			p.exitCode = -1
		}
	}()
	return p.resource(), nil
}

func (ps processes) GetProcess(ctx context.Context, req *ateenvv1alpha.GetProcessRequest) (*ateenvv1alpha.Process, error) {
	env, err := ps.s.guest(ctx)
	if err != nil {
		return nil, err
	}
	p, err := env.process(req.GetProcessId())
	if err != nil {
		return nil, err
	}
	return p.resource(), nil
}

func (ps processes) StreamProcessOutput(req *ateenvv1alpha.StreamProcessOutputRequest, stream grpc.ServerStreamingServer[ateenvv1alpha.ProcessOutput]) error {
	env, err := ps.s.guest(stream.Context())
	if err != nil {
		return err
	}
	p, err := env.process(req.GetProcessId())
	if err != nil {
		return err
	}
	stdout, stderr := req.GetStdoutOffset(), req.GetStderrOffset()
	sent := 0
	for {
		p.mu.Lock()
		out, errOut, exited := tail(p.stdout, stdout), tail(p.stderr, stderr), p.exited
		p.mu.Unlock()
		for _, message := range []*ateenvv1alpha.ProcessOutput{
			{Output: &ateenvv1alpha.ProcessOutput_Stdout{Stdout: out}},
			{Output: &ateenvv1alpha.ProcessOutput_Stderr{Stderr: errOut}},
		} {
			if len(message.GetStdout())+len(message.GetStderr()) == 0 {
				continue
			}
			if ps.s.StreamLimit > 0 && sent >= ps.s.StreamLimit {
				return nil
			}
			if err := stream.Send(message); err != nil {
				return err
			}
			sent++
		}
		stdout += int64(len(out))
		stderr += int64(len(errOut))
		if exited && len(out) == 0 && len(errOut) == 0 {
			return stream.Send(&ateenvv1alpha.ProcessOutput{Output: &ateenvv1alpha.ProcessOutput_Exit{Exit: p.resource()}})
		}
		if !req.GetFollow() && len(out) == 0 && len(errOut) == 0 {
			return nil
		}
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func tail(spooled []byte, offset int64) []byte {
	if offset >= int64(len(spooled)) {
		return nil
	}
	return append([]byte(nil), spooled[offset:]...)
}

func (ps processes) WriteProcessInput(stream grpc.ClientStreamingServer[ateenvv1alpha.WriteProcessInputRequest, ateenvv1alpha.WriteProcessInputResponse]) error {
	env, err := ps.s.guest(stream.Context())
	if err != nil {
		return err
	}
	var p *process
	var written int64
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&ateenvv1alpha.WriteProcessInputResponse{BytesWritten: written})
		}
		if err != nil {
			return err
		}
		if p == nil {
			if p, err = env.process(req.GetProcessId()); err != nil {
				return err
			}
			if p.stdin == nil {
				return status.Errorf(codes.FailedPrecondition, "process %s was started without stdin", p.id)
			}
		}
		n, err := p.stdin.Write(req.GetData())
		written += int64(n)
		if err != nil {
			return status.Errorf(codes.FailedPrecondition, "process %s: %v", p.id, err)
		}
		if req.GetClose() {
			if err := p.stdin.Close(); err != nil {
				return status.Errorf(codes.FailedPrecondition, "process %s: %v", p.id, err)
			}
		}
	}
}

func (ps processes) SignalProcess(ctx context.Context, req *ateenvv1alpha.SignalProcessRequest) (*ateenvv1alpha.Process, error) {
	env, err := ps.s.guest(ctx)
	if err != nil {
		return nil, err
	}
	p, err := env.process(req.GetProcessId())
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	exited := p.exited
	p.mu.Unlock()
	if exited {
		return nil, status.Errorf(codes.FailedPrecondition, "process %s has exited", p.id)
	}
	p.kill(syscall.Signal(req.GetSignal()))
	return p.resource(), nil
}

type files struct {
	ateenvv1alpha.UnimplementedFileSystemServiceServer
	s *Server
}

func (f files) ReadFile(req *ateenvv1alpha.ReadFileRequest, stream grpc.ServerStreamingServer[ateenvv1alpha.ReadFileResponse]) error {
	env, err := f.s.guest(stream.Context())
	if err != nil {
		return err
	}
	file, err := os.Open(env.path(req.GetPath()))
	if errors.Is(err, os.ErrNotExist) {
		return status.Errorf(codes.NotFound, "file %s not found", req.GetPath())
	}
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	defer file.Close()
	chunk := make([]byte, 32*1024)
	for {
		n, err := file.Read(chunk)
		if n > 0 {
			if err := stream.Send(&ateenvv1alpha.ReadFileResponse{Chunk: append([]byte(nil), chunk[:n]...)}); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
}

func (f files) WriteFile(stream grpc.ClientStreamingServer[ateenvv1alpha.WriteFileRequest, ateenvv1alpha.WriteFileResponse]) error {
	env, err := f.s.guest(stream.Context())
	if err != nil {
		return err
	}
	var file *os.File
	var written int64
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			if file == nil {
				return status.Error(codes.InvalidArgument, "WriteFile received no message")
			}
			if err := file.Close(); err != nil {
				return status.Error(codes.Internal, err.Error())
			}
			file = nil
			return stream.SendAndClose(&ateenvv1alpha.WriteFileResponse{BytesWritten: written})
		}
		if err != nil {
			return err
		}
		if file == nil {
			if req.GetPath() == "" {
				return status.Error(codes.InvalidArgument, "the first WriteFile message names no path")
			}
			path := env.path(req.GetPath())
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return status.Error(codes.Internal, err.Error())
			}
			mode := os.FileMode(req.GetMode())
			if mode == 0 {
				mode = 0o644
			}
			if file, err = os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode); err != nil {
				return status.Error(codes.Internal, err.Error())
			}
		}
		n, err := file.Write(req.GetChunk())
		written += int64(n)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
	}
}

// selfSigned is a certificate for 127.0.0.1, which the control plane serves.
func selfSigned(t testing.TB) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the in-process substrate's key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "substratetest"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("sign the in-process substrate's certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
