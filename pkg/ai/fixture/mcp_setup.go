// ABOUTME: MCP HTTP proxy bring-up — parses each run's mcpConfig, starts proxies, rewrites URLs.
// ABOUTME: Stdio MCP servers pass through unchanged; only HTTP servers (those with a "url") get a proxy.

package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai/fixture/mcpproxy"
)

// MCPProxyInfo records one (server name → upstream → proxy URL) mapping for
// each HTTP MCP server we put a reverse proxy in front of.
type MCPProxyInfo struct {
	Server   string `json:"server"`
	Upstream string `json:"upstream"`
	ProxyURL string `json:"proxyUrl"`
}

// setupMCPProxies walks every run's mcpConfig, parses each config, finds HTTP
// MCP servers (those with a "url" field), and spins up a reverse proxy per
// unique upstream URL. Returns the proxies, their (server→upstream→proxy URL)
// summary, and a map of run name → rewritten config file paths that should
// replace the originals when claude is invoked.
//
// Configs that contain only stdio servers (no "url" fields) are left alone:
// no rewrite file is written and the run's MCPConfig is not modified.
func setupMCPProxies(f *Fixture, artifactDir string) (proxies []*mcpproxy.Proxy, infos []MCPProxyInfo, rewrites map[string][]string, err error) {
	rewrites = map[string][]string{}
	proxiesByURL := map[string]*mcpproxy.Proxy{}

	rewriteDir := filepath.Join(artifactDir, ".mcpconfigs")
	dirCreated := false

	for _, raw := range f.Runs {
		merged := f.Merge(raw)
		if len(merged.MCPConfig) == 0 {
			continue
		}
		var rewritten []string
		anyRewrite := false
		for idx, cfg := range merged.MCPConfig {
			data, srcLabel, readErr := readMCPConfigContent(f.Dir, cfg)
			if readErr != nil {
				return nil, nil, nil, fmt.Errorf("reading mcp config %s for %q: %w", srcLabel, merged.Name, readErr)
			}
			if len(data) == 0 {
				rewritten = append(rewritten, cfg)
				continue
			}
			newData, newInfos, rewriteErr := rewriteMCPConfig(data, proxiesByURL, f.MCPProxy.Headers)
			if rewriteErr != nil {
				return nil, nil, nil, fmt.Errorf("rewriting mcp config %s for %q: %w", srcLabel, merged.Name, rewriteErr)
			}
			// Only spend a write if at least one HTTP server was rewritten.
			if len(newInfos) == 0 {
				rewritten = append(rewritten, cfg)
				continue
			}
			infos = append(infos, newInfos...)
			if !dirCreated {
				if mkErr := os.MkdirAll(rewriteDir, 0o755); mkErr != nil {
					return nil, nil, nil, mkErr
				}
				dirCreated = true
			}
			outPath := filepath.Join(rewriteDir, fmt.Sprintf("%s-%d.json", merged.Name, idx+1))
			if writeErr := os.WriteFile(outPath, newData, 0o600); writeErr != nil {
				return nil, nil, nil, writeErr
			}
			rewritten = append(rewritten, outPath)
			anyRewrite = true
		}
		if anyRewrite {
			rewrites[merged.Name] = rewritten
		}
	}

	for _, p := range proxiesByURL {
		proxies = append(proxies, p)
	}
	return proxies, infos, rewrites, nil
}

// readMCPConfigContent loads the contents of one mcpConfig entry, which may be
// either inline JSON or a file path (relative to the fixture dir). srcLabel is
// a human-readable identifier for error messages.
func readMCPConfigContent(fixtureDir, cfg string) (data []byte, srcLabel string, err error) {
	trimmed := strings.TrimSpace(cfg)
	if trimmed == "" {
		return nil, "(empty)", nil
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return []byte(cfg), "(inline)", nil
	}
	path := cfg
	if !filepath.IsAbs(path) {
		path = filepath.Join(fixtureDir, path)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, path, err
	}
	return bytes, path, nil
}

// rewriteMCPConfig parses an mcp config JSON, replaces every HTTP server's URL
// with a proxy URL (creating new proxies as needed via proxiesByURL), and
// returns the rewritten JSON plus info on any new proxies that were created.
func rewriteMCPConfig(data []byte, proxiesByURL map[string]*mcpproxy.Proxy, inject map[string]string) ([]byte, []MCPProxyInfo, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, nil, fmt.Errorf("invalid mcp config json: %w", err)
	}
	serversRaw, ok := top["mcpServers"]
	if !ok {
		return data, nil, nil
	}
	var servers map[string]map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &servers); err != nil {
		return nil, nil, fmt.Errorf("invalid mcpServers map: %w", err)
	}
	var newInfos []MCPProxyInfo
	rewrote := false
	for name, srv := range servers {
		urlRaw, hasURL := srv["url"]
		if !hasURL {
			continue
		}
		var urlStr string
		if err := json.Unmarshal(urlRaw, &urlStr); err != nil || urlStr == "" {
			continue
		}
		p, exists := proxiesByURL[urlStr]
		if !exists {
			np, startErr := mcpproxy.Start(name, urlStr, inject)
			if startErr != nil {
				return nil, nil, fmt.Errorf("starting proxy for mcp server %q: %w", name, startErr)
			}
			proxiesByURL[urlStr] = np
			p = np
			newInfos = append(newInfos, MCPProxyInfo{
				Server:   name,
				Upstream: urlStr,
				ProxyURL: np.URL(),
			})
		}
		newURL, err := json.Marshal(p.URL())
		if err != nil {
			return nil, nil, err
		}
		srv["url"] = newURL
		servers[name] = srv
		rewrote = true
	}
	if !rewrote {
		return data, nil, nil
	}
	newServersRaw, err := json.Marshal(servers)
	if err != nil {
		return nil, nil, err
	}
	top["mcpServers"] = newServersRaw
	out, err := json.Marshal(top)
	if err != nil {
		return nil, nil, err
	}
	return out, newInfos, nil
}
