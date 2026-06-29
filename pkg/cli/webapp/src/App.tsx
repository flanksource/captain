import { useMemo } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
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
          <div className="flex h-screen min-h-0 flex-col bg-background text-foreground">
            <header className="flex h-12 shrink-0 items-center gap-2 border-b border-border bg-card px-3">
              <button
                type="button"
                className="mr-2 text-sm font-semibold"
                onClick={() => router.navigate("/")}
              >
                Captain
              </button>
              <Button
                size="sm"
                variant={route.kind === "launcher" ? "secondary" : "ghost"}
                onClick={() => router.navigate("/")}
              >
                Agent
              </Button>
              <Button
                size="sm"
                variant={route.kind === "operations" ? "secondary" : "ghost"}
                onClick={() => router.navigate("/operations")}
              >
                Operations
              </Button>
              <div className="flex-1" />
              <ThemeSwitcher />
              <DensitySwitcher />
            </header>

            <main className="min-h-0 flex-1 overflow-hidden">
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
            </main>
            <ChatLayer />
          </div>
        </ChatWindowManagerProvider>
      </RouterProvider>
    </QueryClientProvider>
  );
}

type Route =
  | { kind: "launcher" }
  | { kind: "operations" }
  | { kind: "chat"; threadId: string; model?: string };

function parseRoute(pathname: string, search: string): Route {
  if (pathname.startsWith("/operations")) return { kind: "operations" };
  if (pathname.startsWith("/chat/")) {
    const threadId = decodeURIComponent(pathname.slice("/chat/".length).split("/")[0] ?? "");
    const model = new URLSearchParams(search).get("model") || undefined;
    return threadId ? { kind: "chat", threadId, model } : { kind: "launcher" };
  }
  return { kind: "launcher" };
}
