import { useState } from "react";
import { Button, InputField } from "@flanksource/clicky-ui/components";
import { providerLabel, type RuntimeAdapter } from "./WhoamiModel";

type ProviderTokenResponse = {
  provider: string;
  valid: boolean;
  saved: boolean;
  source: string;
  maskedToken: string;
  modelCount: number;
};

type TokenStatus = {
  tone: "success" | "error";
  message: string;
};

export function WhoamiProviderToken({
  runtime,
  onRefresh,
}: {
  runtime: RuntimeAdapter;
  onRefresh: () => Promise<void>;
}) {
  const [token, setToken] = useState("");
  const [pending, setPending] = useState<"test" | "save" | null>(null);
  const [status, setStatus] = useState<TokenStatus | null>(null);
  const candidate = token.trim();

  async function submit(action: "test" | "save") {
    setPending(action);
    setStatus(null);
    let result: ProviderTokenResponse;
    try {
      result = await updateProviderToken(runtime.provider, action, candidate);
    } catch (error) {
      setStatus({ tone: "error", message: errorMessage(error) });
      setPending(null);
      return;
    }
    if (action === "test") {
      setStatus({
        tone: "success",
        message: `${candidate ? "Candidate" : "Current"} token is valid for ${modelCount(result.modelCount)}.`,
      });
      setPending(null);
      return;
    }

    setToken("");
    setStatus({ tone: "success", message: `Token saved and validated against ${modelCount(result.modelCount)}.` });
    try {
      await onRefresh();
    } catch (error) {
      setStatus({ tone: "error", message: `Token saved, but catalog refresh failed: ${errorMessage(error)}` });
    } finally {
      setPending(null);
    }
  }

  return (
    <section className="grid gap-density-3 border-t border-border pt-density-3">
      <div>
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">API credential</h3>
        <p className="mt-density-1 text-xs text-muted-foreground">
          Test a candidate without saving, or validate and store it in Captain&apos;s local vault.
        </p>
      </div>
      <label className="grid gap-density-1 text-xs font-medium" htmlFor={`provider-token-${runtime.provider}`}>
        {providerLabel(runtime.provider)} API token
        <InputField
          id={`provider-token-${runtime.provider}`}
          type="password"
          autoComplete="new-password"
          spellCheck={false}
          value={token}
          disabled={pending !== null}
          placeholder={runtime.authenticated ? "Enter a replacement token" : "Enter an API token"}
          onChange={setToken}
        />
      </label>
      <div className="flex flex-wrap gap-density-2">
        <Button size="sm" variant="outline" disabled={pending !== null}
          onClick={() => void submit("test")}>
          {pending === "test" ? "Testing…" : candidate ? "Test token" : "Test current"}
        </Button>
        <Button size="sm" disabled={pending !== null || candidate === ""}
          onClick={() => void submit("save")}>
          {pending === "save" ? "Saving…" : "Save token"}
        </Button>
      </div>
      {status && (
        <p role={status.tone === "error" ? "alert" : "status"} aria-live="polite"
          className={status.tone === "error" ? "text-xs text-destructive" : "text-xs text-green-700 dark:text-green-400"}>
          {status.message}
        </p>
      )}
    </section>
  );
}

async function updateProviderToken(
  provider: string,
  action: "test" | "save",
  token: string,
): Promise<ProviderTokenResponse> {
  const test = action === "test";
  const response = await fetch(
    `/api/captain/ai/providers/${encodeURIComponent(provider)}/token${test ? "/test" : ""}`,
    {
      method: test ? "POST" : "PUT",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: token === "" ? "{}" : JSON.stringify({ token }),
    },
  );
  if (!response.ok) {
    const message = (await response.text()).trim();
    throw new Error(message || `Token validation failed with ${response.status}`);
  }
  const result: unknown = await response.json();
  if (!isProviderTokenResponse(result)) throw new Error("Provider token response has an invalid shape");
  return result;
}

function isProviderTokenResponse(value: unknown): value is ProviderTokenResponse {
  if (!value || typeof value !== "object") return false;
  const result = value as Record<string, unknown>;
  return typeof result.provider === "string" && typeof result.valid === "boolean" &&
    typeof result.saved === "boolean" && typeof result.source === "string" &&
    typeof result.maskedToken === "string" && typeof result.modelCount === "number";
}

function modelCount(count: number): string {
  return `${count} ${count === 1 ? "model" : "models"}`;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
