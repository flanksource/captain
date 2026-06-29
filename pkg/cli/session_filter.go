package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/flanksource/captain/pkg/claude"
)

var canonicalUUIDArgRE = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func splitSessionIDPathArgs(args []string) (sessionIDs []string, paths []string) {
	for _, arg := range args {
		if isSessionIDArg(arg) {
			sessionIDs = appendUniqueSessionID(sessionIDs, arg)
			continue
		}
		paths = append(paths, arg)
	}
	return sessionIDs, paths
}

func isSessionIDArg(arg string) bool {
	return canonicalUUIDArgRE.MatchString(strings.TrimSpace(arg))
}

func normalizeSessionIDFilters(legacySession, sessionID string, positionalIDs []string) ([]string, error) {
	legacySession = strings.TrimSpace(legacySession)
	sessionID = strings.TrimSpace(sessionID)
	if legacySession != "" && sessionID != "" && legacySession != sessionID {
		return nil, fmt.Errorf("--session and --session-id must match when both are set")
	}

	var ids []string
	ids = appendUniqueSessionID(ids, sessionID)
	ids = appendUniqueSessionID(ids, legacySession)
	for _, id := range positionalIDs {
		ids = appendUniqueSessionID(ids, id)
	}
	return ids, nil
}

func appendUniqueSessionID(ids []string, id string) []string {
	id = normalizeSessionID(id)
	if id == "" {
		return ids
	}
	for _, existing := range ids {
		if existing == id {
			return ids
		}
	}
	return append(ids, id)
}

func normalizeSessionID(id string) string {
	id = strings.TrimSpace(id)
	if isSessionIDArg(id) {
		return strings.ToLower(id)
	}
	return id
}

func firstSessionID(sessionIDs []string) string {
	if len(sessionIDs) == 0 {
		return ""
	}
	return sessionIDs[0]
}

func filterCostsBySessionID(costs []claude.SessionCost, sessionIDs []string) []claude.SessionCost {
	if len(sessionIDs) == 0 {
		return costs
	}
	filter := claude.Filter{SessionIDs: sessionIDs}
	out := make([]claude.SessionCost, 0, len(costs))
	for _, cost := range costs {
		if filter.MatchesSessionID(cost.SessionID) {
			out = append(out, cost)
		}
	}
	return out
}

func historyScanCWD(defaultCWD string, paths []string, searchAll bool) string {
	if searchAll {
		return defaultCWD
	}

	var scoped string
	for _, path := range paths {
		root := projectRootForPathArg(path)
		if root == "" {
			continue
		}
		if scoped == "" {
			scoped = root
			continue
		}
		if canonicalPath(scoped) != canonicalPath(root) {
			return defaultCWD
		}
	}
	if scoped != "" {
		return scoped
	}
	return defaultCWD
}

func projectRootForPathArg(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "*?[]") {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(abs)
	if err != nil {
		return ""
	}
	dir := abs
	if !info.IsDir() {
		dir = filepath.Dir(abs)
	}
	projectInfo := claude.FindProjectInfo(dir)
	if projectInfo.MarkerFile == "" {
		return ""
	}
	return projectInfo.Root
}
