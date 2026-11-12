#!/usr/bin/env bash
# Captures the pinned native configuration sources that `go run ./hack/adapterresources resources`
# turns into the published native resource schemas. Run from the repository root.
set -euo pipefail

CODEX_VERSION=rust-v0.164.0
GEMINI_VERSION=v0.64.0
CLAUDE_AGENT_SDK_VERSION=0.3.280
TS_JSON_SCHEMA_GENERATOR_VERSION=2.9.0

sources=.tmp/adapter-schema-sources
mkdir -p "$sources"

curl -fsSL "https://raw.githubusercontent.com/openai/codex/${CODEX_VERSION}/codex-rs/core/config.schema.json" -o "$sources/codex-config.json"
curl -fsSL "https://raw.githubusercontent.com/google-gemini/gemini-cli/${GEMINI_VERSION}/schemas/settings.schema.json" -o "$sources/gemini-settings.json"

command npm install --silent --no-audit --no-fund --ignore-scripts --prefix "$sources/sdk" \
  "@anthropic-ai/claude-agent-sdk@${CLAUDE_AGENT_SDK_VERSION}" "ts-json-schema-generator@${TS_JSON_SCHEMA_GENERATOR_VERSION}"

for resource in Settings:settings Agent:agent MCP:mcp Plugin:plugin; do
  "$sources/sdk/node_modules/.bin/ts-json-schema-generator" --path hack/adapterresources/claude-types.ts \
    --type "CaptainClaude${resource%%:*}" --out "$sources/claude-${resource##*:}.json"
done
