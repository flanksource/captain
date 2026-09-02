import path from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { clickyPackageRoot, clickySourceAliases } from "./vite.config";

// CAPTAIN_UI_CLICKY_SOURCE=1 resolves @flanksource/clicky-ui from the sibling
// checkout, the way the dev server does, so tests can exercise exports that
// are not in the published package yet.
const useClickySource = process.env.CAPTAIN_UI_CLICKY_SOURCE === "1";

// The sibling checkout's dependencies live in its own pnpm store. Node would
// load them natively with their own React copy, so anything resolved from a
// node_modules other than this webapp's is inlined through Vite, where
// `resolve.dedupe` pins React to the copy the tests render with.
const ownModules = path.resolve(__dirname, "node_modules");
const foreignModules = new RegExp(
  `^(?!${ownModules.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}).*/node_modules/`,
);

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: clickySourceAliases(useClickySource),
    dedupe: ["react", "react-dom", "@tanstack/react-query", "monaco-editor"],
  },
  ...(useClickySource
    ? { server: { fs: { allow: [__dirname, clickyPackageRoot] } } }
    : {}),
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    ...(useClickySource
      ? { server: { deps: { inline: [foreignModules] } } }
      : {}),
  },
});
