import type {
  Settings,
  AgentDefinition,
  McpServerConfigForProcessTransport,
  SdkPluginConfig,
} from "../../.tmp/adapter-schema-sources/sdk/node_modules/@anthropic-ai/claude-agent-sdk/sdk";

export type CaptainClaudeSettings = Settings;
export type CaptainClaudeAgent = AgentDefinition;
export type CaptainClaudeMCP = McpServerConfigForProcessTransport;
export type CaptainClaudePlugin = SdkPluginConfig;
