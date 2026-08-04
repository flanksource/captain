package cli

import (
	"context"
	"embed"
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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
	"github.com/flanksource/clicky/rpc"
	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/clicky/task"
	"github.com/spf13/cobra"
)

// The built webapp under webapp/dist is generated locally and ignored. The
// tracked .gitkeep lets the embed pattern compile before `task www:build`
// generates the Vite assets. all: ensures the placeholder is embedded too.
// When index.html is absent, serve.go reports it at runtime.
//
//go:embed all:webapp/dist
var captainWebappFS embed.FS

type ServeOptions struct {
	Host       string
	Port       int
	Dev        bool
	UIPort     int
	Open       bool
	PromptDirs []string
	MCPServers []aichat.MCPServer
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
	chat, mcpTools, err := newCaptainChatService(ctx, rootCmd, opts, cwd, threadStore, authority, attachmentStore)
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
	mux.HandleFunc("GET /api/entities", rpcServer.HandleEntities)
	mux.HandleFunc("GET /health", rpcServer.HandleHealth)
	rpcServer.RegisterExecutionRoutes(mux)
	mux.HandleFunc("POST /api/captain/chat/threads/from-agent", handleThreadFromAgent(threadStore))
	mux.HandleFunc("GET /api/captain/projects", handleProjects())
	mux.HandleFunc("GET /api/captain/sessions/live", handleSessionsLive())
	mux.HandleFunc("GET /api/captain/sessions/throughput", handleSessionsThroughput())
	mux.HandleFunc("GET /api/captain/sessions/{id}", handleSessionGet())
	mux.HandleFunc("POST /api/captain/hooks/{provider}", handleMonitorHookEvent())
	mux.HandleFunc("GET /api/captain/ai/permissions/catalog", handlePermissionCatalog(cwd))
	mux.HandleFunc("GET /api/captain/ai/prompt/schema", handlePromptSchema())
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
	mux.Handle("/api/chat", chat.Handler())
	mux.Handle("/api/chat/", chat.Handler())

	uiHandler, err := newCaptainWebappHandler()
	if err != nil {
		return err
	}
	mux.Handle("/", uiHandler)

	// Export the serve URL so every captain-launched agent (and the hook
	// receiver subprocesses its sessions spawn) delivers hook events to this
	// instance even off the default port.
	if err := os.Setenv(api.ServeURLEnv, "http://"+addr); err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:        addr,
		Handler:     rpchttp.TimingMiddleware(PromptDirsMiddleware(mux, opts.PromptDirs)),
		ReadTimeout: 30 * time.Second,
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
	dsn, source := captainDatabaseIdentity()
	log.Infof("Database Info: source=%q dsn=%q live_sessions=%d", source, database.MaskDSN(dsn), liveSessions)

	go prunePromptRuns(ctx, promptRuns)
	defer promptChats.stopAll()

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(stdout, "Captain API listening on http://%s\n", addr)
		fmt.Fprintf(stdout, "  UI:           http://%s/\n", addr)
		fmt.Fprintf(stdout, "  OpenAPI JSON: http://%s/api/openapi.json\n", addr)
		fmt.Fprintf(stdout, "  AI Chat:      http://%s/api/chat\n", addr)
		if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
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

func captainWebappDevDir() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate webapp source")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "webapp")
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return "", fmt.Errorf("webapp/package.json not found at %s: %w", dir, err)
	}
	return dir, nil
}

func newCaptainWebappHandler() (http.Handler, error) {
	sub, err := fs.Sub(captainWebappFS, "webapp/dist")
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
