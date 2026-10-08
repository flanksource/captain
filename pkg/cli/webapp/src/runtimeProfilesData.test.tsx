import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { usePromptRuntimeProfiles } from "./runtimeProfilesData";

const DB_SOURCE = {
  kind: "db",
  id: "db",
  label: "captain database",
  writable: true,
  records: ["preset", "profile"],
};

const PRESET = {
  id: "review-preset",
  key: "review",
  name: "Plan and review",
  scope: "surface",
  spec: {},
  presets: [],
  source: DB_SOURCE,
  updatedAt: "2026-09-01T10:00:00Z",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("usePromptRuntimeProfiles", () => {
  it("passes presets to the editor without loading profile APIs", async () => {
    const fetchMock = vi.fn(async (url: string) =>
      jsonResponse(url === "/api/v1/runtime-preset" ? [PRESET] : []),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => usePromptRuntimeProfiles(), {
      wrapper: wrapper(),
    });

    await waitFor(() =>
      expect(result.current.editorProps.presets).toEqual([PRESET]),
    );
    expect(result.current.error).toBeNull();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/runtime-preset",
      expect.anything(),
    );
  });

  it("withholds presets and reports the failure when the server has no preset entity", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        url === "/api/v1/runtime-preset"
          ? new Response("404 page not found", { status: 404 })
          : jsonResponse([]),
      ),
    );
    const { result } = renderHook(() => usePromptRuntimeProfiles(), {
      wrapper: wrapper(),
    });

    await waitFor(() => expect(result.current.error).toBeInstanceOf(Error));
    expect((result.current.error as Error).message).toBe("404 page not found");
    expect(result.current.editorProps).not.toHaveProperty("presets");
  });
});

function wrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return ({ children }: PropsWithChildren) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

function jsonResponse(value: unknown) {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
