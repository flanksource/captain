import { useMemo, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  AppShell,
  Button,
  DensitySwitcher,
  ThemeSwitcher,
} from "@flanksource/clicky-ui/components";
import {
  RouterProvider,
  useBrowserRouter,
} from "@flanksource/clicky-ui/rpc";
import { ChatWindowManagerProvider } from "@flanksource/clicky-ui/ai";
import { EntityExplorerApp } from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { AgentLauncher } from "./AgentLauncher";
import { ChatLayer } from "./ChatLayer";
import { ChatRoute } from "./ChatRoute";
import { PromptWorkbench } from "./PromptWorkbench";
import { SessionBrowser } from "./SessionBrowser";

export function App() {
  const queryClient = useMemo(
    () =>
      new QueryClient({
        defaultOptions: { queries: { retry: false, gcTime: 5 * 60 * 1000 } },
      }),
    [],
  );
  const router = useBrowserRouter();
  const route = parseRoute(router.pathname, window.location.search);

  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider adapter={router}>
        <ChatWindowManagerProvider storageId="captain-chat">
          {route.kind === "sessions" ? (
            <SessionBrowser
              selectedId={route.sessionId}
              onNavigate={router.navigate}
              nav={<CaptainNav active="sessions" onNavigate={router.navigate} />}
              actions={<ShellActions />}
            />
          ) : route.kind === "prompts" ? (
            <PromptWorkbench
              selectedId={route.promptId}
              onNavigate={router.navigate}
              nav={<CaptainNav active="prompts" onNavigate={router.navigate} />}
              actions={<ShellActions />}
            />
          ) : (
            <CaptainShell
              active={route.kind === "operations" ? "operations" : "agent"}
              onNavigate={router.navigate}
            >
              {route.kind === "operations" ? (
                <EntityExplorerApp
                  client={apiClient}
                  basePath="/operations"
                  showApiExplorer
                />
              ) : route.kind === "chat" ? (
                <ChatRoute
                  threadId={route.threadId}
                  model={route.model}
                  onNavigate={router.navigate}
                />
              ) : (
                <div className="h-full overflow-auto">
                  <AgentLauncher onNavigate={router.navigate} />
                </div>
              )}
            </CaptainShell>
          )}
          <ChatLayer />
        </ChatWindowManagerProvider>
      </RouterProvider>
    </QueryClientProvider>
  );
}

type PrimaryRoute = "agent" | "sessions" | "prompts" | "operations";

function CaptainShell({
  active,
  onNavigate,
  children,
}: {
  active: PrimaryRoute;
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
  children: ReactNode;
}) {
  return (
    <AppShell
      className="h-screen"
      brand={<ShellBrand onNavigate={onNavigate} />}
      nav={<CaptainNav active={active} onNavigate={onNavigate} />}
      actions={<ShellActions />}
      contentClassName="p-0 overflow-hidden"
    >
      {children}
    </AppShell>
  );
}

function ShellBrand({
  onNavigate,
}: {
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
}) {
  return (
    <button
      type="button"
      className="text-sm font-semibold"
      onClick={() => onNavigate("/")}
    >
      Captain
    </button>
  );
}

function CaptainNav({
  active,
  onNavigate,
}: {
  active: PrimaryRoute;
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
}) {
  return (
    <div className="flex items-center gap-1">
      <Button
        size="sm"
        variant={active === "agent" ? "secondary" : "ghost"}
        onClick={() => onNavigate("/")}
      >
        Agent
      </Button>
      <Button
        size="sm"
        variant={active === "sessions" ? "secondary" : "ghost"}
        onClick={() => onNavigate("/sessions")}
      >
        Sessions
      </Button>
      <Button
        size="sm"
        variant={active === "prompts" ? "secondary" : "ghost"}
        onClick={() => onNavigate("/prompts")}
      >
        Prompts
      </Button>
      <Button
        size="sm"
        variant={active === "operations" ? "secondary" : "ghost"}
        onClick={() => onNavigate("/operations")}
      >
        Operations
      </Button>
    </div>
  );
}

function ShellActions() {
  return (
    <>
      <ThemeSwitcher />
      <DensitySwitcher />
    </>
  );
}

type Route =
  | { kind: "launcher" }
  | { kind: "sessions"; sessionId?: string }
  | { kind: "prompts"; promptId?: string }
  | { kind: "operations" }
  | { kind: "chat"; threadId: string; model?: string };

function parseRoute(pathname: string, search: string): Route {
  if (pathname.startsWith("/operations")) return { kind: "operations" };
  if (pathname.startsWith("/prompts")) {
    const raw = pathname.slice("/prompts".length).replace(/^\/+/, "");
    const promptId = raw ? decodeURIComponent(raw.split("/")[0] ?? "") : undefined;
    return promptId ? { kind: "prompts", promptId } : { kind: "prompts" };
  }
  if (pathname.startsWith("/sessions")) {
    const raw = pathname.slice("/sessions".length).replace(/^\/+/, "");
    const sessionId = raw ? decodeURIComponent(raw.split("/")[0] ?? "") : undefined;
    return sessionId ? { kind: "sessions", sessionId } : { kind: "sessions" };
  }
  if (pathname.startsWith("/chat/")) {
    const threadId = decodeURIComponent(pathname.slice("/chat/".length).split("/")[0] ?? "");
    const model = new URLSearchParams(search).get("model") || undefined;
    return threadId ? { kind: "chat", threadId, model } : { kind: "launcher" };
  }
  return { kind: "launcher" };
}
