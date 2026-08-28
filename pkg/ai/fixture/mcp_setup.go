// ABOUTME: MCP HTTP proxy bring-up — parses each run's mcpConfig, starts proxies, rewrites URLs.
// ABOUTME: Stdio MCP servers pass through unchanged; only HTTP servers (those with a "url") get a proxy.

package fixture

import (
	"encoding/json"
	"fmt"
	"net/url"
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
	return rewriteMCPConfigWithProxyKey(data, proxiesByURL, inject, func(_ string, upstreamURL string) string {
		return upstreamURL
	})
}

func rewriteMCPConfigWithProxyKey(
	data []byte,
	proxies map[string]*mcpproxy.Proxy,
	inject map[string]string,
	proxyKey func(server, upstreamURL string) string,
) ([]byte, []MCPProxyInfo, error) {
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
		key := proxyKey(name, urlStr)
		p, exists := proxies[key]
		if !exists {
			np, startErr := mcpproxy.Start(name, urlStr, inject)
			if startErr != nil {
				return nil, nil, fmt.Errorf("starting proxy for mcp server %q: %w", name, startErr)
			}
			proxies[key] = np
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

// MCPConfigCapture owns the temporary rewritten configs and HTTP proxies for
// one observation run. HasUncaptured is true when a config also contains a
// non-HTTP server such as stdio, whose traffic cannot cross the proxy.
type MCPConfigCapture struct {
	Configs       []string
	HasHTTP       bool
	HasUncaptured bool

	proxies []*mcpproxy.Proxy
	tempDir string
}

// StartMCPConfigCapture rewrites one run's explicit HTTP MCP servers through
// Captain proxies. Rewritten configs stay in a mode-0700 temporary directory
// because they may contain credentials and are removed by Close.
func StartMCPConfigCapture(
	configs []string,
	baseDir string,
	observe func(mcpproxy.ObservationEvent),
) (*MCPConfigCapture, error) {
	capture := &MCPConfigCapture{}
	if len(configs) == 0 {
		return capture, nil
	}
	tempDir, err := os.MkdirTemp("", "captain-observe-mcp-")
	if err != nil {
		return nil, fmt.Errorf("create MCP capture directory: %w", err)
	}
	capture.tempDir = tempDir
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			capture.Close()
		}
	}()

	proxiesByIdentity := map[string]*mcpproxy.Proxy{}
	for index, config := range configs {
		data, _, err := readMCPConfigContent(baseDir, config)
		if err != nil {
			return nil, fmt.Errorf("read MCP config %d: %w", index+1, err)
		}
		hasHTTP, hasUncaptured, err := mcpConfigTransports(data)
		if err != nil {
			return nil, fmt.Errorf("inspect MCP config %d: %w", index+1, err)
		}
		capture.HasHTTP = capture.HasHTTP || hasHTTP
		capture.HasUncaptured = capture.HasUncaptured || hasUncaptured
		rewritten, _, err := rewriteMCPConfigWithProxyKey(data, proxiesByIdentity, nil, func(server, upstreamURL string) string {
			return server + "\x00" + upstreamURL
		})
		if err != nil {
			return nil, fmt.Errorf("rewrite MCP config %d: %w", index+1, err)
		}
		path := filepath.Join(tempDir, fmt.Sprintf("config-%d.json", index+1))
		if err := os.WriteFile(path, rewritten, 0o600); err != nil {
			return nil, fmt.Errorf("write MCP config %d: %w", index+1, err)
		}
		capture.Configs = append(capture.Configs, path)
	}
	for _, proxy := range proxiesByIdentity {
		proxy.SetObserver(observe)
		capture.proxies = append(capture.proxies, proxy)
	}
	cleanupOnError = false
	return capture, nil
}

// Close stops every proxy and removes credential-bearing rewritten configs.
func (c *MCPConfigCapture) Close() {
	if c == nil {
		return
	}
	for _, proxy := range c.proxies {
		proxy.Close()
	}
	c.proxies = nil
	if c.tempDir != "" {
		_ = os.RemoveAll(c.tempDir)
		c.tempDir = ""
	}
}

func mcpConfigTransports(data []byte) (hasHTTP, hasUncaptured bool, err error) {
	var config struct {
		Servers map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return false, false, fmt.Errorf("invalid JSON: %w", err)
	}
	for name, server := range config.Servers {
		raw, ok := server["url"]
		if !ok {
			hasUncaptured = true
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || value == "" {
			return false, false, fmt.Errorf("HTTP server %q has an invalid URL", name)
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return false, false, fmt.Errorf("HTTP server %q has an invalid or credential-bearing URL", name)
		}
		hasHTTP = true
	}
	return hasHTTP, hasUncaptured, nil
}
