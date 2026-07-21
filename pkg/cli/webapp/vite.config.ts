import path from "node:path";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const apiTarget = process.env.CAPTAIN_API_URL || "http://localhost:8080";
const clickyPackageRoot = path.resolve(__dirname, "../../../../clicky-ui/packages/ui");
const clickySourceRoot = path.resolve(clickyPackageRoot, "src");

export default defineConfig(({ command }) => {
  const useClickySource =
    command === "serve" && process.env.CAPTAIN_UI_CLICKY_SOURCE !== "0";

  return {
    base: "/",
    define: useClickySource ? clickyVersionDefines() : {},
    plugins: [tailwindcss(), react()],
    resolve: {
      alias: [
        ...clickySourceAliases(useClickySource),
        { find: "@", replacement: path.resolve(__dirname, "./src") },
      ],
      dedupe: ["react", "react-dom", "@tanstack/react-query"],
    },
    server: {
      fs: useClickySource ? { allow: [__dirname, clickyPackageRoot] } : undefined,
      proxy: {
        "/api": apiTarget,
        "/health": apiTarget,
      },
    },
    build: {
      outDir: "dist",
      emptyOutDir: true,
    },
  };
});

function clickySourceAliases(enabled: boolean) {
  if (!enabled) return [];
  return [
    {
      find: "@flanksource/clicky-ui/styles.css",
      replacement: path.resolve(clickySourceRoot, "styles/full.css"),
    },
    {
      find: "@flanksource/clicky-ui/tailwind-preset",
      replacement: path.resolve(clickySourceRoot, "tailwind-preset.ts"),
    },
    {
      find: "@flanksource/clicky-ui/components",
      replacement: path.resolve(clickySourceRoot, "components.ts"),
    },
    {
      find: "@flanksource/clicky-ui/clicky",
      replacement: path.resolve(clickySourceRoot, "clicky.ts"),
    },
    {
      find: "@flanksource/clicky-ui/icons",
      replacement: path.resolve(clickySourceRoot, "icons.ts"),
    },
    {
      find: "@flanksource/clicky-ui/hooks",
      replacement: path.resolve(clickySourceRoot, "hooks.ts"),
    },
    {
      find: "@flanksource/clicky-ui/mdx-editor.css",
      replacement: path.resolve(clickySourceRoot, "styles/mdx-editor.css"),
    },
    {
      find: "@flanksource/clicky-ui/mdx-editor",
      replacement: path.resolve(clickySourceRoot, "mdx-editor.ts"),
    },
    {
      find: "@flanksource/clicky-ui/utils",
      replacement: path.resolve(clickySourceRoot, "utils.ts"),
    },
    {
      find: "@flanksource/clicky-ui/data",
      replacement: path.resolve(clickySourceRoot, "data.ts"),
    },
    {
      find: "@flanksource/clicky-ui/chat",
      replacement: path.resolve(clickySourceRoot, "chat.ts"),
    },
    {
      find: "@flanksource/clicky-ui/ai",
      replacement: path.resolve(clickySourceRoot, "ai.ts"),
    },
    {
      find: "@flanksource/clicky-ui/rpc",
      replacement: path.resolve(clickySourceRoot, "rpc.ts"),
    },
    {
      find: "@flanksource/clicky-ui",
      replacement: path.resolve(clickySourceRoot, "index.ts"),
    },
  ];
}

function clickyVersionDefines() {
  const version = readClickyPackageVersion();
  return {
    __CLICKY_COMMIT__: JSON.stringify(git(["rev-parse", "--short", "HEAD"], "")),
    __CLICKY_TAG__: JSON.stringify(`clicky-ui@${version}`),
    __CLICKY_DATE__: JSON.stringify(new Date().toISOString()),
    __CLICKY_DIRTY__: JSON.stringify(git(["status", "--porcelain"], "").length > 0),
  };
}

function readClickyPackageVersion() {
  try {
    const raw = readFileSync(path.resolve(clickyPackageRoot, "package.json"), "utf8");
    const parsed = JSON.parse(raw) as { version?: string };
    return parsed.version || "dev";
  } catch {
    return "dev";
  }
}

function git(args: string[], fallback: string) {
  try {
    // Pass arguments as an argv array via execFileSync so no shell is spawned,
    // avoiding shell interpretation of the (filesystem-derived) repo path.
    return execFileSync("git", ["-C", clickyPackageRoot, ...args], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    return fallback;
  }
}
