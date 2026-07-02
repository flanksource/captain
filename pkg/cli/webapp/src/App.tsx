import { useMemo, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppShell } from "@flanksource/clicky-ui/components";
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
import { HomeDashboard } from "./HomeDashboard";
import { PromptWorkbench } from "./PromptWorkbench";
import { SessionBrowser } from "./SessionBrowser";
import {
  CAPTAIN_SIDEBAR_COLLAPSE_KEY,
  ShellActions,
  captainNavSections,
  type PrimaryRoute,
} from "./shell";

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
              navSections={captainNavSections("sessions")}
              actions={<ShellActions />}
            />
          ) : route.kind === "prompts" ? (
            <PromptWorkbench
              selectedId={route.promptId}
              onNavigate={router.navigate}
              navSections={captainNavSections("prompts")}
              actions={<ShellActions />}
            />
          ) : (
            <CaptainShell active={primaryRoute(route)} onNavigate={router.navigate}>
              {route.kind === "dashboard" ? (
                <HomeDashboard onNavigate={router.navigate} />
              ) : route.kind === "operations" ? (
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
      navSections={captainNavSections(active)}
      collapsedStorageKey={CAPTAIN_SIDEBAR_COLLAPSE_KEY}
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

type Route =
  | { kind: "dashboard" }
  | { kind: "agent" }
  | { kind: "sessions"; sessionId?: string }
  | { kind: "prompts"; promptId?: string }
  | { kind: "operations" }
  | { kind: "chat"; threadId: string; model?: string };

function primaryRoute(route: Route): PrimaryRoute {
  if (route.kind === "dashboard") return "dashboard";
  if (route.kind === "operations") return "operations";
  if (route.kind === "prompts") return "prompts";
  if (route.kind === "sessions") return "sessions";
  return "agent";
}

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
    return threadId ? { kind: "chat", threadId, model } : { kind: "dashboard" };
  }
  if (pathname.startsWith("/agent")) return { kind: "agent" };
  return { kind: "dashboard" };
}
