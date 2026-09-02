package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type captainUIHandlerOptions struct {
	Dev     bool
	ViteURL string
}

func newCaptainUIHandler(opts captainUIHandlerOptions) (http.Handler, error) {
	if !opts.Dev {
		return newCaptainWebappHandler()
	}
	if strings.TrimSpace(opts.ViteURL) == "" {
		return nil, fmt.Errorf("vite development URL is required")
	}
	target, err := url.Parse(opts.ViteURL)
	if err != nil {
		return nil, fmt.Errorf("parse Vite development URL %q: %w", opts.ViteURL, err)
	}
	if target.Scheme != "http" || target.Host == "" {
		return nil, fmt.Errorf("invalid Vite development URL %q", opts.ViteURL)
	}
	return httputil.NewSingleHostReverseProxy(target), nil
}

type viteDevServerOptions struct {
	APIHost string
	APIPort int
	UIPort  int
	Stdout  io.Writer
	Stderr  io.Writer
}

type captainViteDevServer struct {
	cmd *exec.Cmd
	url string
}

func startCaptainViteDevServer(ctx context.Context, opts viteDevServerOptions) (*captainViteDevServer, error) {
	webappDir, err := captainWebappDevDir()
	if err != nil {
		return nil, err
	}
	// Wildcard bind addresses are not dialable Vite proxy targets.
	targetHost := opts.APIHost
	switch targetHost {
	case "0.0.0.0", "":
		targetHost = "127.0.0.1"
	case "::":
		targetHost = "::1"
	}
	apiURL := "http://" + net.JoinHostPort(targetHost, strconv.Itoa(opts.APIPort))
	args, uiPort, err := viteDevServerArgs(opts.UIPort)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "pnpm", args...)
	cmd.Dir = webappDir
	cmd.Env = append(os.Environ(), "CAPTAIN_API_URL="+apiURL)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start vite dev server in %s: %w", webappDir, err)
	}
	fmt.Fprintf(opts.Stdout, "  Dev API proxy: /api -> %s\n", apiURL)
	return &captainViteDevServer{
		cmd: cmd,
		url: "http://" + net.JoinHostPort("localhost", strconv.Itoa(uiPort)),
	}, nil
}

func (s *captainViteDevServer) stop() {
	if s == nil || s.cmd == nil {
		return
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Cancel()
	}
	_ = s.cmd.Wait()
}

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
