import { useCallback, useMemo, useState, useSyncExternalStore } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AppShell, type AppShellProps } from "@flanksource/clicky-ui/components";
import {
  RouterProvider,
  useBrowserRouter,
} from "@flanksource/clicky-ui/rpc";
import { ChatWindowManagerProvider } from "@flanksource/clicky-ui/ai";
import { EntityExplorerApp } from "@flanksource/clicky-ui/rpc";
import { apiClient } from "./api";
import { AgentLauncher } from "./AgentLauncher";
import { ChatLayer } from "./ChatLayer";
import {
  CommandPalette,
  SearchTrigger,
  useCommandPaletteShortcut,
} from "./CommandPalette";
import { ChatRoute } from "./ChatRoute";
import { HomeDashboard } from "./HomeDashboard";
import { PromptWorkbench } from "./PromptWorkbench";
import { SessionBrowser } from "./SessionBrowser";
import {
  getSessionListSearchSnapshot,
  parseSessionListFilters,
  subscribeSessionListSearch,
} from "./sessionListFilters";
import { ShellActions } from "./shell";
import { SandboxesPage } from "./SandboxesPage";
import { WhoamiPage } from "./WhoamiPage";
import {
  captainNavSections,
  CAPTAIN_SIDEBAR_COLLAPSE_KEY,
  getProjectScopeSnapshot,
  setProjectScopeInLocation,
  subscribeProjectScope,
  withProjectScope,
  type PrimaryRoute,
} from "./shellHelpers";
import type { ProjectScope } from "./sessionData";

export function App() {
  const queryClient = useMemo(
    () =>
      new QueryClient({
        defaultOptions: { queries: { retry: false, gcTime: 5 * 60 * 1000 } },
      }),
    [],
  );
  const router = useBrowserRouter();
  const locationSearch = useSyncExternalStore(
    subscribeSessionListSearch,
    getSessionListSearchSnapshot,
    getSessionListSearchSnapshot,
  );
  const route = parseRoute(router.pathname, locationSearch);
  const projectScope = useSyncExternalStore(
    subscribeProjectScope,
    getProjectScopeSnapshot,
    getProjectScopeSnapshot,
  );

  const setProjectScope = (scope: ProjectScope) => {
    setProjectScopeInLocation(scope, router.navigate, router.pathname, locationSearch);
  };
  const shellActions = (
    <ShellActions projectScope={projectScope} onProjectScopeChange={setProjectScope} />
  );

  const [paletteOpen, setPaletteOpen] = useState(false);
  useCommandPaletteShortcut(
    useCallback(() => setPaletteOpen((prev) => !prev), []),
  );
  const shellSearch = <SearchTrigger onOpen={() => setPaletteOpen(true)} />;

  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider adapter={router}>
        <ChatWindowManagerProvider storageId="captain-chat">
          {route.kind === "sessions" ? (
            <SessionBrowser
              selectedId={route.sessionId}
              onNavigate={router.navigate}
              navSections={captainNavSections("sessions", projectScope)}
              actions={shellActions}
              search={shellSearch}
              projectScope={projectScope}
              filters={parseSessionListFilters(locationSearch)}
            />
          ) : route.kind === "prompts" ? (
            <PromptWorkbench
              selectedId={route.promptId}
              onNavigate={router.navigate}
              navSections={captainNavSections("prompts", projectScope)}
              actions={shellActions}
              search={shellSearch}
            />
          ) : (
            <CaptainShell
              active={primaryRoute(route)}
              onNavigate={router.navigate}
              projectScope={projectScope}
              actions={shellActions}
              search={shellSearch}
            >
              {route.kind === "dashboard" ? (
                <HomeDashboard onNavigate={router.navigate} projectScope={projectScope} />
              ) : route.kind === "whoami" ? (
                <WhoamiPage />
              ) : route.kind === "sandboxes" ? (
                <SandboxesPage />
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
          <CommandPalette
            open={paletteOpen}
            onClose={() => setPaletteOpen(false)}
            onNavigate={router.navigate}
          />
        </ChatWindowManagerProvider>
      </RouterProvider>
    </QueryClientProvider>
  );
}

function CaptainShell({
  active,
  onNavigate,
  projectScope,
  actions,
  search,
  children,
}: {
  active: PrimaryRoute;
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
  projectScope: ProjectScope;
  actions: AppShellProps["actions"];
  search: AppShellProps["search"];
  children: AppShellProps["children"];
}) {
  return (
    <AppShell
      className="h-screen"
      brand={<ShellBrand onNavigate={onNavigate} projectScope={projectScope} />}
      navSections={captainNavSections(active, projectScope)}
      collapsedStorageKey={CAPTAIN_SIDEBAR_COLLAPSE_KEY}
      actions={actions}
      search={search}
      contentClassName="p-0 overflow-hidden"
    >
      {children}
    </AppShell>
  );
}

function ShellBrand({
  onNavigate,
  projectScope,
}: {
  onNavigate: (to: string, opts?: { replace?: boolean }) => void;
  projectScope: ProjectScope;
}) {
  return (
    <button
      type="button"
      className="text-sm font-semibold"
      onClick={() => onNavigate(withProjectScope("/", projectScope))}
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
  | { kind: "whoami" }
  | { kind: "sandboxes" }
  | { kind: "operations" }
  | { kind: "chat"; threadId: string; model?: string };

function primaryRoute(route: Route): PrimaryRoute {
  if (route.kind === "dashboard") return "dashboard";
  if (route.kind === "operations") return "operations";
  if (route.kind === "whoami") return "whoami";
  if (route.kind === "sandboxes") return "sandboxes";
  if (route.kind === "prompts") return "prompts";
  if (route.kind === "sessions") return "sessions";
  return "agent";
}

function parseRoute(pathname: string, search: string): Route {
  if (pathname.startsWith("/operations")) return { kind: "operations" };
  if (pathname.startsWith("/whoami")) return { kind: "whoami" };
  if (pathname.startsWith("/sandboxes")) return { kind: "sandboxes" };
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
