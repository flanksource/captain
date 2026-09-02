import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { usePromptRuntimeProfiles } from "./runtimeProfilesData";

const DB_SOURCE = { kind: "db", id: "db", label: "captain database", writable: true, records: ["preset", "profile"] };

const PROFILE = {
  id: "review-profile",
  key: "review",
  name: "Plan and review",
  spec: {},
  presets: [],
  source: DB_SOURCE,
  updatedAt: "2026-09-01T10:00:00Z",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("usePromptRuntimeProfiles", () => {
  it("passes both lists to the editor once loaded and saves through the database target", async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") return jsonResponse({ ...PROFILE, ...JSON.parse(String(init.body)) });
      if (init?.method === "POST") return jsonResponse({ ...PROFILE, id: "profile-new", ...JSON.parse(String(init.body)) });
      return jsonResponse(url === "/api/v1/runtime-profile" ? [PROFILE] : []);
    });
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => usePromptRuntimeProfiles(), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.editorProps.profiles).toEqual([PROFILE]));
    expect(result.current.editorProps.presets).toEqual([]);
    expect(result.current.error).toBeNull();

    const saved = await result.current.editorProps.onSaveProfile({ ...PROFILE, name: "Review" });
    expect(saved.name).toBe("Review");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/runtime-profile",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({ id: "review-profile", name: "Review", spec: {}, presets: [] }),
      }),
    );

    const created = await result.current.editorProps.onCreateProfile({ ...PROFILE, id: "draft", name: "Review copy" });
    expect(created.id).toBe("profile-new");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/runtime-profile",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ target: "db", name: "Review copy", spec: {}, presets: [] }),
      }),
    );
  });

  it("withholds the lists and reports the failure when the server has no profile entity", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string) =>
        url === "/api/v1/runtime-profile"
          ? new Response("404 page not found", { status: 404 })
          : jsonResponse([]),
      ),
    );
    const { result } = renderHook(() => usePromptRuntimeProfiles(), { wrapper: wrapper() });

    await waitFor(() => expect(result.current.error).toBeInstanceOf(Error));
    expect((result.current.error as Error).message).toBe("404 page not found");
    expect(result.current.editorProps).not.toHaveProperty("profiles");
    expect(result.current.editorProps).not.toHaveProperty("presets");
  });
});

function wrapper() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
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
