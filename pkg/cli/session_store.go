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
	"sync"
	"syscall"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	commonsdb "github.com/flanksource/commons-db/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StoredSession is one persisted transcript summary — a root session or a
// sub-agent (linked by ParentID) — invalidated by the file's ModUnix+Size. The
// rich fields are stored as Postgres jsonb.
type StoredSession struct {
	Path      string  `gorm:"primaryKey;column:path"`
	ID        string  `gorm:"index;column:id"`
	ParentID  *string `gorm:"index;column:parent_id"`
	Source    string  `gorm:"column:source"`
	IsAgent   bool    `gorm:"column:is_agent"`
	AgentType string  `gorm:"column:agent_type"`
	AgentDesc string  `gorm:"column:agent_desc"`

	ModUnix        int64 `gorm:"column:mod_unix"`
	Size           int64 `gorm:"column:size"`
	SummaryVersion int   `gorm:"column:summary_version"`

	Project       string `gorm:"column:project"`
	CWD           string `gorm:"column:cwd"`
	Model         string `gorm:"column:model"`
	Title         string `gorm:"column:title"`
	InitialPrompt string `gorm:"column:initial_prompt"`

	Git      session.GitState `gorm:"serializer:json;type:jsonb;column:git"`
	Provider ProviderInfo     `gorm:"serializer:json;type:jsonb;column:provider"`

	StartedAt *time.Time `gorm:"column:started_at"`
	EndedAt   *time.Time `gorm:"column:ended_at"`

	Cost      api.Cost              `gorm:"serializer:json;type:jsonb;column:cost"`
	Usage     api.Usage             `gorm:"serializer:json;type:jsonb;column:usage"`
	Files     session.ChangedFiles  `gorm:"serializer:json;type:jsonb;column:files"`
	Approvals session.ApprovalStats `gorm:"serializer:json;type:jsonb;column:approvals"`

	ToolCalls     int `gorm:"column:tool_calls"`
	MessageCount  int `gorm:"column:message_count"`
	ContextTokens int `gorm:"column:context_tokens"`

	Slug     string        `gorm:"column:slug"`
	PlanPath string        `gorm:"column:plan_path"`
	PlanSlug string        `gorm:"column:plan_slug"`
	Plan     *session.Plan `gorm:"serializer:json;type:jsonb;column:plan"`

	UpdatedAt time.Time
}

func (StoredSession) TableName() string { return "captain_sessions" }

const sessionSummaryVersion = 4

// ProviderInfo is the provider block stored as jsonb.
type ProviderInfo struct {
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Backend         string `json:"backend,omitempty"`
}

// StoredPrompt is the realized prompt for a captain-launched session, keyed by
// session id and written by the prompt-run path.
type StoredPrompt struct {
	SessionID string             `gorm:"primaryKey;column:session_id"`
	RunID     string             `gorm:"column:run_id"`
	Model     string             `gorm:"column:model"`
	Backend   string             `gorm:"column:backend"`
	Realized  PromptRenderResult `gorm:"serializer:json;type:jsonb;column:realized"`
	CreatedAt time.Time
}

func (StoredPrompt) TableName() string { return "captain_session_prompts" }

// sessionDB wraps the gorm handle; a nil *sessionDB means "no store — uncached".
type sessionDB struct{ gdb *gorm.DB }

// sessionStoreState serializes the choice between the legacy summary cache and
// Captain's authoritative native database. The two schemas both use the
// captain_sessions name but have intentionally different models, so they must
// never be active on the same connection.
type sessionStoreState struct {
	once sync.Once
	mu   sync.RWMutex

	legacy *sessionDB
	native *gorm.DB
}

var sessionStores sessionStoreState

const (
	captainSessionEnvDSN = "CAPTAIN_SESSION_DB_URL"
	gavelDBEnvDSN        = "GAVEL_DB_DSN"
	gavelCacheEnvDSN     = "GAVEL_GITHUB_CACHE_DSN"

	gavelDBModeDSN      = "dsn"
	gavelDBModeEmbedded = "embedded"
)

// sessionStore returns the persistent session store, opening it once. It returns
// nil (and logs a single Warn) when the DB is unavailable, so callers degrade to
// uncached summarization.
func sessionStore() *sessionDB {
	return sessionStores.get(openSessionStore)
}

// ConfigureNativeDatabase records the authoritative Captain connection used by
// the host application. It must be called before the legacy session cache has
// opened. Once configured, session summary and realized-prompt callers degrade
// to their existing uncached paths instead of running the incompatible legacy
// AutoMigrate or querying native Captain tables.
//
// Native session repository wiring is deliberately separate from this guard;
// callers should use database.Open to migrate/reuse the shared pool, then call
// ConfigureNativeDatabase with that handle's Gorm connection.
func ConfigureNativeDatabase(gormDB *gorm.DB) error {
	return sessionStores.configureNative(gormDB)
}

func (s *sessionStoreState) configureNative(gormDB *gorm.DB) error {
	if gormDB == nil {
		return errors.New("native Captain database GORM pool is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.legacy != nil {
		return errors.New("legacy Captain session cache is already initialized")
	}
	if s.native != nil {
		if s.native == gormDB {
			return nil
		}
		return errors.New("native Captain database is already configured with a different pool")
	}
	s.native = gormDB
	return nil
}

func (s *sessionStoreState) nativeDatabase() *gorm.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.native
}

func (s *sessionStoreState) get(opener func() *sessionDB) *sessionDB {
	s.once.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.native == nil {
			s.legacy = opener()
		}
	})

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.native != nil {
		return nil
	}
	return s.legacy
}

func openSessionStore() *sessionDB {
	dsn, source, disabled, err := configuredSessionDSN()
	if disabled {
		return nil // explicitly disabled (tests, or users who opt out)
	}
	if err != nil {
		log.Warnf("gavel session store unavailable: %v; falling back to captain session store", err)
	}
	if dsn == "" {
		dir, err := sessionDBDir()
		if err != nil {
			// WORKAROUND(session-cache): user-approved degrade-to-uncached when the
			// summary DB can't be opened; summarization still works, just uncached.
			log.Warnf("session store unavailable: %v; continuing uncached", err)
			return nil
		}
		embeddedDSN, _, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{DataDir: dir})
		if err != nil {
			log.Warnf("session store unavailable: %v; continuing uncached", err)
			return nil
		}
		dsn = embeddedDSN // shared daemon: leave running, don't call stop()
		source = "captain embedded session DB"
	}
	log.Debugf("session store using %s", source)
	gdb, pool, err := commonsdb.SetupDB(dsn, "session-cache")
	if err != nil {
		log.Warnf("session store unavailable: %v; continuing uncached", err)
		return nil
	}
	// SetupDB uses the pgx pool to validate connectivity but GORM owns a separate
	// database/sql pool. The legacy cache only retains the latter.
	if pool != nil {
		pool.Close()
	}
	return openOwnedLegacySessionStore(gdb)
}

func openOwnedLegacySessionStore(gdb *gorm.DB) *sessionDB {
	store := openLegacySessionStore(gdb)
	if store != nil {
		return store
	}
	if sqlDB, err := gdb.DB(); err != nil {
		log.Warnf("access unused legacy session database: %v", err)
	} else if err := sqlDB.Close(); err != nil {
		log.Warnf("close unused legacy session database: %v", err)
	}
	return nil
}

func openLegacySessionStore(gdb *gorm.DB) *sessionDB {
	if hasAuthoritativeSessionSchema(gdb) {
		log.Warnf("authoritative Captain database detected; legacy session cache disabled")
		return nil
	}
	if err := gdb.AutoMigrate(&StoredSession{}, &StoredPrompt{}); err != nil {
		log.Warnf("session store migrate failed: %v; continuing uncached", err)
		return nil
	}
	return &sessionDB{gdb: gdb}
}

// hasAuthoritativeSessionSchema uses lifecycle columns that never existed in
// the legacy summary cache as an unambiguous guard. Metadata inspection is safe;
// no native Captain rows are queried.
func hasAuthoritativeSessionSchema(gdb *gorm.DB) bool {
	if gdb == nil {
		return false
	}
	migrator := gdb.Migrator()
	return migrator.HasColumn("captain_sessions", "lifecycle_status") &&
		migrator.HasColumn("captain_sessions", "activity_state") &&
		migrator.HasColumn("captain_sessions", "health_state")
}

func configuredSessionDSN() (dsn string, source string, disabled bool, err error) {
	if dsn := strings.TrimSpace(os.Getenv(gavelDBEnvDSN)); dsn != "" {
		return dsn, gavelDBEnvDSN, false, nil
	}

	if dsn := strings.TrimSpace(os.Getenv(gavelCacheEnvDSN)); dsn != "" {
		return dsn, gavelCacheEnvDSN, false, nil
	}

	if dsn := os.Getenv(captainSessionEnvDSN); dsn != "" {
		if dsn == "off" {
			return "", "", true, nil
		}
		return dsn, captainSessionEnvDSN, false, nil
	}

	dsn, source, err = gavelConfiguredSessionDSN()
	return dsn, source, false, err
}

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

func gavelSessionDSN() (string, string, error) {
	if dsn := strings.TrimSpace(os.Getenv(gavelDBEnvDSN)); dsn != "" {
		return dsn, gavelDBEnvDSN, nil
	}
	if dsn := strings.TrimSpace(os.Getenv(gavelCacheEnvDSN)); dsn != "" {
		return dsn, gavelCacheEnvDSN, nil
	}
	return gavelConfiguredSessionDSN()
}

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

// lookupFresh returns the stored row for path when it exists and its mtime+size
// still match the file (a fresh cache hit).
func (s *sessionDB) lookupFresh(path string, modUnix, size int64) (*StoredSession, bool) {
	var row StoredSession
	if err := s.gdb.Where("path = ?", path).First(&row).Error; err != nil {
		return nil, false
	}
	if row.ModUnix != modUnix || row.Size != size || row.SummaryVersion != sessionSummaryVersion {
		return nil, false
	}
	return &row, true
}

// upsertRows persists each Row (one transcript), stamping the file's mtime+size.
func (s *sessionDB) upsertRows(rows []session.Row) {
	for _, r := range rows {
		stored, ok := storedFromRow(r)
		if !ok {
			continue
		}
		if err := s.gdb.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "path"}},
			UpdateAll: true,
		}).Create(&stored).Error; err != nil {
			log.Warnf("session store upsert %s: %v", r.Path, err)
		}
	}
}

// prompt returns the realized-prompt record for a session id, if any.
func (s *sessionDB) prompt(sessionID string) (*StoredPrompt, bool) {
	var p StoredPrompt
	if err := s.gdb.Where("session_id = ?", sessionID).First(&p).Error; err != nil {
		return nil, false
	}
	return &p, true
}

// upsertPrompt records the realized prompt for a launched session.
func (s *sessionDB) upsertPrompt(p StoredPrompt) {
	if err := s.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "session_id"}},
		UpdateAll: true,
	}).Create(&p).Error; err != nil {
		log.Warnf("session store upsert prompt %s: %v", p.SessionID, err)
	}
}

// storedBase maps a session.Row to a StoredSession without the file identity
// (ModUnix/Size) — the fields shared by persistence and projection.
func storedBase(r session.Row) StoredSession {
	var parent *string
	if r.ParentID != "" {
		p := r.ParentID
		parent = &p
	}
	row := StoredSession{
		Path:           r.Path,
		ID:             r.ID,
		ParentID:       parent,
		Source:         r.Source,
		IsAgent:        r.IsAgent,
		AgentType:      r.AgentType,
		AgentDesc:      r.AgentDesc,
		SummaryVersion: sessionSummaryVersion,
		Project:        r.Project,
		CWD:            r.CWD,
		Model:          r.Model,
		Title:          r.Title,
		InitialPrompt:  r.InitialPrompt,
		Git:            r.Git,
		Provider:       ProviderInfo{Name: r.Provider, Version: r.Version, ReasoningEffort: r.ReasoningEffort},
		StartedAt:      r.StartedAt,
		EndedAt:        r.EndedAt,
		Cost:           r.Cost,
		Usage:          r.Usage,
		Files:          r.Files,
		Approvals:      r.Approvals,
		ToolCalls:      r.ToolCalls,
		MessageCount:   r.Messages,
		ContextTokens:  r.ContextTokens,
		Slug:           r.Slug,
	}
	if r.Plan != nil {
		row.PlanPath = r.Plan.Path
		row.PlanSlug = r.Plan.Slug
		plan := *r.Plan
		row.Plan = &plan
	}
	return row
}

// storedFromRow maps a session.Row to a StoredSession, stamping the transcript
// file's mtime+size. Returns ok=false when the file can't be stat'd.
func storedFromRow(r session.Row) (StoredSession, bool) {
	info, err := os.Stat(r.Path)
	if err != nil {
		return StoredSession{}, false
	}
	row := storedBase(r)
	row.ModUnix = info.ModTime().UnixNano()
	row.Size = info.Size()
	return row, true
}

const (
	claudeContextWindow = 1_000_000
	codexContextWindow  = 200_000
)

// contextWindow returns the model context window for a source.
func contextWindow(source string) int {
	if source == "codex" {
		return codexContextWindow
	}
	return claudeContextWindow
}

// freeContextPercent is 100 minus the used fraction of the window, clamped.
func freeContextPercent(used, window int) int {
	if window <= 0 {
		return 0
	}
	if used < 0 {
		used = 0
	}
	free := 100 - int(float64(used)/float64(window)*100)
	if free < 0 {
		return 0
	}
	if free > 100 {
		return 100
	}
	return free
}

// recordFromRow projects a session.Row straight to a SessionRecord (miss path,
// where the store may be unavailable).
func recordFromRow(r session.Row) SessionRecord {
	return storedBase(r).toRecord()
}

// toRecord projects a StoredSession to the SessionRecord list/live wire shape.
func (row StoredSession) toRecord() SessionRecord {
	rec := SessionRecord{
		Key:             sessionRecordKey(row.Source, row.Path),
		ID:              row.ID,
		Source:          row.Source,
		Project:         row.Project,
		Slug:            row.Slug,
		Title:           row.Title,
		InitialPrompt:   row.InitialPrompt,
		Model:           row.Model,
		ReasoningEffort: row.Provider.ReasoningEffort,
		Version:         row.Provider.Version,
		Provider:        row.Provider.Name,
		GitBranch:       row.Git.Branch,
		CWD:             row.CWD,
		StartedAt:       row.StartedAt,
		EndedAt:         row.EndedAt,
		ToolCalls:       row.ToolCalls,
		Messages:        row.MessageCount,
		DetailAvailable: true,
		CostUSD:         row.Cost.Total(),
	}
	if u := row.Usage; u.TotalTokens() > 0 {
		rec.Tokens = &SessionTokensWire{
			InputTokens:         u.InputTokens,
			OutputTokens:        u.OutputTokens,
			CacheReadTokens:     u.CacheReadTokens,
			CacheCreationTokens: u.CacheWriteTokens,
			TotalTokens:         u.TotalTokens(),
		}
	}
	if row.ContextTokens > 0 {
		window := contextWindow(row.Source)
		rec.Context = &SessionContextWire{
			UsedTokens:   row.ContextTokens,
			WindowTokens: window,
			FreePercent:  freeContextPercent(row.ContextTokens, window),
		}
	}
	return rec
}
