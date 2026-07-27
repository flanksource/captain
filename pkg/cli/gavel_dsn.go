package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
)

const (
	captainSessionEnvDSN = "CAPTAIN_SESSION_DB_URL"
	gavelDBEnvDSN        = "GAVEL_DB_DSN"
	gavelCacheEnvDSN     = "GAVEL_GITHUB_CACHE_DSN"

	gavelDBModeDSN      = "dsn"
	gavelDBModeEmbedded = "embedded"
)

// sessionDBDir is the embedded-postgres data directory (shared across processes).
func sessionDBDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "captain", "session-db"), nil
}

type gavelDBConfig struct {
	Mode string `json:"mode"`
	DSN  string `json:"dsn,omitempty"`
}

// gavelConfiguredSessionDSN resolves a gavel-shared database from
// ~/.config/gavel/db.json: an explicit DSN, or gavel's embedded postgres
// (reusing a running instance before starting one).
func gavelConfiguredSessionDSN() (string, string, error) {
	cfg, path, err := loadGavelDBConfig()
	if err != nil {
		return "", "", err
	}
	switch cfg.Mode {
	case "":
		return "", "", nil
	case gavelDBModeDSN:
		if strings.TrimSpace(cfg.DSN) == "" {
			return "", "", fmt.Errorf("%s has mode=%s but empty dsn", path, gavelDBModeDSN)
		}
		return cfg.DSN, path, nil
	case gavelDBModeEmbedded:
		running, err := findRunningGavelEmbeddedPostgres()
		if err != nil {
			return "", "", err
		}
		if running != nil {
			return gavelEmbeddedDSN(running.Port), path, nil
		}
		dataDir, err := gavelEmbeddedDataDir()
		if err != nil {
			return "", "", err
		}
		dsn, _, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
			DataDir:  dataDir,
			Database: "gavel",
		})
		if err != nil {
			return "", "", err
		}
		return dsn, path, nil
	default:
		return "", "", fmt.Errorf("%s has unsupported mode %q", path, cfg.Mode)
	}
}

func loadGavelDBConfig() (gavelDBConfig, string, error) {
	path, err := gavelDBConfigPath()
	if err != nil {
		return gavelDBConfig{}, "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return gavelDBConfig{}, path, nil
		}
		return gavelDBConfig{}, path, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg gavelDBConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return gavelDBConfig{}, path, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, path, nil
}

func gavelDBConfigPath() (string, error) {
	dir, err := gavelStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "db.json"), nil
}

func gavelEmbeddedDataDir() (string, error) {
	dir, err := gavelStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "embedded-pg"), nil
}

func gavelStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".config", "gavel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create gavel state dir %s: %w", dir, err)
	}
	return dir, nil
}

type runningGavelEmbeddedPostgres struct {
	PID  int
	Port int
}

func findRunningGavelEmbeddedPostgres() (*runningGavelEmbeddedPostgres, error) {
	dataDir, err := gavelEmbeddedDataDir()
	if err != nil {
		return nil, err
	}
	pidPath := filepath.Join(dataDir, "data", "postmaster.pid")
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", pidPath, err)
	}
	lines := strings.Split(string(raw), "\n")
	const postmasterLinePort = 3
	if len(lines) <= postmasterLinePort {
		return nil, fmt.Errorf("%s has %d lines, need >%d", pidPath, len(lines), postmasterLinePort)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return nil, fmt.Errorf("%s: invalid pid %q: %w", pidPath, lines[0], err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[postmasterLinePort]))
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("%s: invalid port %q: %w", pidPath, lines[postmasterLinePort], err)
	}
	if !processAlive(pid) || !tcpPortReachable("localhost", port) {
		return nil, nil
	}
	return &runningGavelEmbeddedPostgres{PID: pid, Port: port}, nil
}

func gavelEmbeddedDSN(port int) string {
	return fmt.Sprintf("postgres://postgres:postgres@localhost:%d/gavel?sslmode=disable", port)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func tcpPortReachable(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
