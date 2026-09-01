package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/cli/webapp"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/captain/pkg/monitor"
	"github.com/flanksource/clicky/rpc"
	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/clicky/task"
	"github.com/spf13/cobra"
)

type ServeOptions struct {
	Host       string
	Port       int
	Dev        bool
	UIPort     int
	Open       bool
	PromptDirs []string
	MCPServers []aichat.MCPServer
	// TLS serves HTTPS, generating and reusing a self-signed certificate
	// beside the git-agent keys. It is off by default because the ordinary
	// case is a loopback UI, and turning every local URL into https would
	// mean a certificate warning for no gain.
	TLS bool
	// TLSCert and TLSKey supply a real certificate instead of the generated
	// one. Both or neither.
	TLSCert string
	TLSKey  string
	// TLSHosts are the addresses agents will reach this server on. They are
	// added to a generated certificate's subject names, and checked against a
	// supplied one — a certificate that omits the address agents dial fails at
	// the client, and by then every agent is already enrolled against it.
	TLSHosts []string
}

func NewServeCommand(version string) *cobra.Command {
	opts := ServeOptions{
		Host:   "localhost",
		Port:   9020,
		UIPort: 0,
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Captain agent launcher UI",
		Long: `Start Captain's HTTP API and web UI.

The API exposes Clicky RPC endpoints at /api/openapi.json and /api/v1/..., plus
the AI chat backend at /api/chat. The UI launches the existing "captain ai agent"
operation and opens follow-up chat windows that resume the returned agent
session.

With --dev, Captain also starts the Vite dev server from pkg/cli/webapp and
proxies /api back to this Go process.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.validate(); err != nil {
				return err
			}
			return RunServe(cmd.Context(), cmd.Root(), opts, version, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}

	cmd.Flags().StringVar(&opts.Host, "host", opts.Host, "Host to bind the API server to")
	cmd.Flags().IntVarP(&opts.Port, "port", "p", opts.Port, "Port to bind the API server to")
	cmd.Flags().BoolVar(&opts.Dev, "dev", false, "Launch the Vite dev server with /api proxied to Captain")
	cmd.Flags().IntVar(&opts.UIPort, "ui-port", opts.UIPort, "Port for the Vite dev server when --dev is set (random by default)")
	cmd.Flags().BoolVar(&opts.Open, "open", false, "Open the web UI in the default browser")
	cmd.Flags().StringArrayVar(&opts.PromptDirs, "prompt-dir", nil, "Additional local directory containing .prompt files (repeatable)")
	cmd.Flags().BoolVar(&opts.TLS, "tls", false, "Serve HTTPS, generating and reusing a self-signed certificate beside the git-agent keys")
	cmd.Flags().StringVar(&opts.TLSCert, "tls-cert", "", "PEM certificate to serve instead of the generated one")
	cmd.Flags().StringVar(&opts.TLSKey, "tls-key", "", "PEM private key for --tls-cert")
	cmd.Flags().StringArrayVar(&opts.TLSHosts, "tls-host", nil, "Address agents will reach this server on; added to the certificate (repeatable)")

	return cmd
}

func RunServe(ctx context.Context, rootCmd *cobra.Command, opts ServeOptions, version string, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	db, err := captainServeDB(ctx)
	if err != nil {
		return err
	}
	threadStore, err := aichat.NewDatabaseThreadStore(db)
	if err != nil {
		return err
	}
	authority, err := aichat.NewDatabaseExecutionAuthority(db)
	if err != nil {
		return err
	}
	attachmentStore, err := newAttachmentStore(cwd)
	if err != nil {
		return err
	}
	listener, addr, servePort, err := listenCaptainServer(opts.Host, opts.Port)
	if err != nil {
		return err
	}
	defer listener.Close()
	// Resolved once, before anything is registered: the certificate decides both
	// what this server presents and what a joining agent is told to pin, and
	// resolving it twice could hand out one that is not the one being served.
	certificate, err := serveCertificate(opts)
	if err != nil {
		return err
	}
	openAPIConfig := &rpc.OpenAPIConfig{
		Title:       "Captain",
		Description: "Captain command and agent launcher API.",
		Version:     version,
	}
	serveConfig := &rpc.ServeConfig{
		Host:        opts.Host,
		Port:        servePort,
		Title:       openAPIConfig.Title,
		Description: openAPIConfig.Description,
		Version:     openAPIConfig.Version,
		Executor: &rpc.ExecutorConfig{
			Enabled:    true,
			SkipPreRun: true,
			PathPrefix: "/api/v1",
		},
	}

	rpcServer := rpc.NewSwaggerServer(serveConfig, rootCmd, openAPIConfig)
	openAPISpec, err := rpc.NewOpenAPIGenerator(openAPIConfig).GenerateFromCobraWithConfig(rootCmd, rpcServer.ConverterConfig())
	if err != nil {
		return err
	}
	addCaptainPromptRunPaths(openAPISpec)
	addCaptainProviderTokenPaths(openAPISpec)
	addCaptainProviderDefaultsPaths(openAPISpec)
	addCaptainDisabledPaths(openAPISpec)
	chat, mcpTools, err := newCaptainChatService(ctx, rootCmd, opts, cwd, authority, attachmentStore)
	if err != nil {
		return err
	}
	defer func() {
		if err := mcpTools.Close(); err != nil {
			log.Errorf("close Captain MCP tools: %v", err)
		}
	}()

	// Prompt runs are tracked as clicky task groups. Disable terminal rendering:
	// serve runs on a TTY, so otherwise the task manager draws progress bars over
	// the server log. Concurrency is the global manager's worker pool (4), which
	// is plenty for parallel prompt runs.
	task.SetNoRender(true)

	mux := http.NewServeMux()
	mux.Handle("GET /api/openapi.json", handleCaptainOpenAPI(openAPISpec, false))
	mux.Handle("GET /api/openapi.yaml", handleCaptainOpenAPI(openAPISpec, true))
	mux.HandleFunc("GET /health", rpcServer.HandleHealth)
	rpcServer.RegisterExecutionRoutes(mux)
	mux.HandleFunc("POST /api/captain/chat/threads/from-agent", handleThreadFromAgent(threadStore))
	mux.HandleFunc("GET /api/captain/contexts", handleContexts())
	mux.HandleFunc("GET /api/captain/projects", handleProjects())
	mux.HandleFunc("GET /api/captain/sessions/live", handleSessionsLive())
	mux.HandleFunc("GET /api/captain/sessions/throughput", handleSessionsThroughput())
	mux.HandleFunc("POST /api/captain/hooks/{provider}", handleMonitorHookEvent())
	mux.HandleFunc("GET /api/captain/ai/permissions/catalog", handlePermissionCatalog(cwd))
	mux.HandleFunc("GET /api/captain/ai/prompt/schema", handlePromptSchema())
	registerSandboxHandlers(mux)
	registerProviderTokenHandlers(mux)
	registerProviderDefaultsHandlers(mux)
	registerDisabledHandlers(mux)
	mux.HandleFunc("GET /api/captain/secrets/resources", handleSecretResources())
	mux.HandleFunc("GET /api/captain/secrets/preview", handleSecretPreview())
	mux.HandleFunc("POST /api/attachments", handleAttachmentUpload(attachmentStore))
	mux.HandleFunc("GET /api/attachments/{id}", handleAttachmentGet(attachmentStore))
	// Task tracking: /api/captain/tasks, /tasks/stream, /tasks/{id}, and the
	// /tasks/runs/stream run-list SSE the clicky-ui useTaskRuns hook subscribes to.
	task.RegisterHandlers(mux, "/api/captain")
	// Live session history for a prompt run.
	mux.Handle("GET /api/captain/prompt/runs/{runId}/stream", handlePromptRunStream(promptRuns))
	mux.Handle("GET /api/captain/prompt/runs/{runId}", handlePromptRunSnapshot(promptRuns))
	mux.Handle("POST /api/captain/prompt/runs/{runId}/message", handlePromptRunMessage(promptChats))
	mux.Handle("POST /api/captain/prompt/runs/{runId}/interrupt", handlePromptRunInterrupt(promptChats))
	mux.Handle("POST /api/captain/prompt/runs/{runId}/stop", handlePromptRunStop(promptRuns, promptChats))
	mux.Handle("POST /api/captain/sessions/{id}/message", handleSessionMessage(promptChats))
	chatHandler := chat.Handler()
	mux.Handle("/api/chat", chatHandler)
	mux.Handle("/api/chat/", chatHandler)

	// The supervisor's git-agent mailbox, hosted here rather than in its own
	// process: this is the process that holds the database the tokens live in.
	if err := registerGitHandlers(mux, db, addr, certificate); err != nil {
		return err
	}

	uiHandler, err := newCaptainWebappHandler()
	if err != nil {
		return err
	}
	// Keep API matching separate so unknown paths and method mismatches cannot
	// fall through to the SPA's client-side routing fallback.
	root := http.NewServeMux()
	root.Handle("/api/", mux)
	root.Handle("/health", mux)
	root.Handle("/", uiHandler)

	tlsConfig := serveTLSConfig(certificate)
	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
	}
	// Export the serve URL so every captain-launched agent (and the hook
	// receiver subprocesses its sessions spawn) delivers hook events to this
	// instance even off the default port.
	if err := os.Setenv(api.ServeURLEnv, scheme+"://"+addr); err != nil {
		return err
	}
	// Auth sits outside the database-context middleware so an unauthenticated
	// request is refused before it can resolve a context or open a pool.
	auth := TokenAuthMiddleware(TokenAuthConfig{
		Verifier: captaintoken.NewVerifier(db.LookupAPIToken),
		Touch:    db.TouchAPIToken,
	})
	httpSrv := &http.Server{
		Addr: addr,
		Handler: rpchttp.TimingMiddleware(
			auth(DatabaseContextMiddleware(PromptDirsMiddleware(root, opts.PromptDirs)))),
		TLSConfig: tlsConfig,
		// Bounded at the headers rather than the whole request: a synchronous
		// relay runs the supervisor's hook set inside the push, and a prompt
		// hook can take minutes. A 30s whole-request deadline would kill it
		// mid-verdict and report it to the agent as a broken connection.
		ReadHeaderTimeout: 30 * time.Second,
		// /api/chat streams SSE; a fixed write timeout truncates long turns.
		IdleTimeout: 60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	mon, err := monitor.New(monitor.Config{DB: db, HostID: captainHostID()})
	if err != nil {
		return err
	}
	setServeMonitor(mon)
	go func() {
		if err := mon.Run(ctx); err != nil {
			log.Errorf("session monitor stopped: %v", err)
		}
	}()
	select {
	case <-mon.Ready():
	case <-ctx.Done():
		return ctx.Err()
	}
	liveSessions, err := db.CountLiveRootSessions(ctx)
	if err != nil {
		return err
	}
	dsn, source := contextDatabaseIdentity(defaultDatabaseContextName)
	log.Infof("Database Info: source=%q dsn=%q live_sessions=%d", source, database.MaskDSN(dsn), liveSessions)
	if contexts, err := databaseContexts(); err != nil {
		// Reads can select a context per request, so a malformed context
		// configuration is a startup error rather than a surprise mid-session.
		return err
	} else if len(contexts) > 1 {
		log.Infof("Read-only database contexts: %s", strings.Join(databaseContextNames(contexts[1:]), ", "))
	}

	go prunePromptRuns(ctx, promptRuns)
	defer promptChats.stopAll()

	if err := startCredentialPublisher(ctx, stdout); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(stdout, "Captain API listening on %s://%s\n", scheme, addr)
		fmt.Fprintf(stdout, "  UI:           %s://%s/\n", scheme, addr)
		fmt.Fprintf(stdout, "  OpenAPI JSON: %s://%s/api/openapi.json\n", scheme, addr)
		fmt.Fprintf(stdout, "  AI Chat:      %s://%s/api/chat\n", scheme, addr)
		fmt.Fprintf(stdout, "  git-agent:    %s://%s%s\n", scheme, addr, gitagent.GitHTTPPrefix)
		// ServeTLS with an already-configured TLSConfig: the certificate comes
		// from serveTLSConfig, which reuses one rather than issuing per start.
		serve := httpSrv.Serve
		if tlsConfig != nil {
			serve = func(l net.Listener) error { return httpSrv.ServeTLS(l, "", "") }
		}
		if err := serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	var vite *exec.Cmd
	if opts.Dev {
		vite, err = startCaptainViteDevServer(ctx, viteDevServerOptions{
			APIHost: opts.Host,
			APIPort: servePort,
			UIPort:  opts.UIPort,
			Open:    opts.Open,
			Stdout:  stdout,
			Stderr:  stderr,
		})
		if err != nil {
			_ = httpSrv.Close()
			return err
		}
		defer func() {
			if vite.Process != nil {
				_ = vite.Cancel()
			}
			_ = vite.Wait()
		}()
	}

	if opts.Open && !opts.Dev {
		openURL := fmt.Sprintf("http://%s/", addr)
		go func() {
			time.Sleep(500 * time.Millisecond)
			if err := rpc.OpenBrowser(openURL); err != nil {
				fmt.Fprintf(stderr, "failed to open browser: %v\n", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

type viteDevServerOptions struct {
	APIHost string
	APIPort int
	UIPort  int
	Open    bool
	Stdout  io.Writer
	Stderr  io.Writer
}

func startCaptainViteDevServer(ctx context.Context, opts viteDevServerOptions) (*exec.Cmd, error) {
	webappDir, err := captainWebappDevDir()
	if err != nil {
		return nil, err
	}
	// A wildcard bind is not a dialable target, so map it to the loopback address
	// of the same family — mapping :: to 127.0.0.1 would leave Vite unable to
	// reach an API listening only on IPv6. JoinHostPort brackets IPv6 literals,
	// which a bare host:port concatenation would produce invalidly.
	targetHost := opts.APIHost
	switch targetHost {
	case "0.0.0.0", "":
		targetHost = "127.0.0.1"
	case "::":
		targetHost = "::1"
	}
	apiURL := "http://" + net.JoinHostPort(targetHost, strconv.Itoa(opts.APIPort))
	args, err := viteDevServerArgs(opts.UIPort, opts.Open)
	if err != nil {
		return nil, err
	}
	vite := exec.CommandContext(ctx, "pnpm", args...)
	vite.Dir = webappDir
	vite.Env = append(os.Environ(), "CAPTAIN_API_URL="+apiURL)
	vite.Stdout = opts.Stdout
	vite.Stderr = opts.Stderr
	vite.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	vite.Cancel = func() error {
		if vite.Process == nil {
			return nil
		}
		return syscall.Kill(-vite.Process.Pid, syscall.SIGTERM)
	}
	vite.WaitDelay = 5 * time.Second
	if err := vite.Start(); err != nil {
		return nil, fmt.Errorf("start vite dev server in %s: %w", webappDir, err)
	}
	fmt.Fprintf(opts.Stdout, "  Dev API proxy: /api -> %s\n", apiURL)
	return vite, nil
}

// captainWebappDevDir resolves the Vite source tree from the working directory
// rather than runtime.Caller: under -trimpath the caller's file is the import
// path, not a filesystem location, so anchoring to it silently resolves against
// the process CWD. Dev mode only runs from inside a checkout, so the repo root
// is the right anchor.
func captainWebappDevDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locate webapp source: %w", err)
	}
	root := repoRoot(cwd)
	if root == "" {
		return "", fmt.Errorf("locate webapp source: %s is not inside the captain repository", cwd)
	}
	dir := filepath.Join(root, "pkg", "cli", "webapp")
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return "", fmt.Errorf("webapp/package.json not found at %s: %w", dir, err)
	}
	return dir, nil
}

func newCaptainWebappHandler() (http.Handler, error) {
	sub, err := fs.Sub(webapp.FS, "dist")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested := strings.TrimPrefix(r.URL.Path, "/")
		if requested == "" {
			serveCaptainIndex(w, sub)
			return
		}
		if _, err := fs.Stat(sub, requested); err != nil {
			if !looksLikeAssetRequest(requested) {
				serveCaptainIndex(w, sub)
				return
			}
			http.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	}), nil
}

func serveCaptainIndex(w http.ResponseWriter, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "webapp index.html missing; run pnpm --dir pkg/cli/webapp build", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func looksLikeAssetRequest(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, ".")
}

func writeServeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
