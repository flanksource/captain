import { useQuery } from "@tanstack/react-query";
import type { WhoamiResult } from "./WhoamiModel";

export const WHOAMI_CATALOG_URL = "/api/v1/whoami?models=true&limit=0&disabled=true";
export const WHOAMI_CATALOG_QUERY_KEY = ["whoami", "catalog"] as const;

export function useWhoamiCatalog() {
  return useQuery({
    queryKey: WHOAMI_CATALOG_QUERY_KEY,
    queryFn: fetchWhoamiCatalog,
    staleTime: 30_000,
  });
}

export async function fetchWhoamiCatalog(): Promise<WhoamiResult> {
  const response = await fetch(WHOAMI_CATALOG_URL, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: "{}",
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || `Whoami failed with ${response.status}`);
  }
  const result: unknown = await response.json();
  if (!isWhoamiCatalog(result)) {
    throw new Error("Whoami response must include models and runtimes arrays");
  }
  return result;
}

function isWhoamiCatalog(value: unknown): value is WhoamiResult {
  if (!value || typeof value !== "object") return false;
  const result = value as Record<string, unknown>;
  return Array.isArray(result.adapters) && Array.isArray(result.models) && Array.isArray(result.runtimes);
}
