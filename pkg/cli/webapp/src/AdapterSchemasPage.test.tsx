import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState, type PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AdapterSchemasPage } from "./AdapterSchemasPage";
import { parseAdapterSchemaPath, type AdapterSchemaDocument } from "./adapterSchemas";

vi.mock("@flanksource/clicky-ui/data", () => ({
  SchemaViewer: ({ schema }: { schema: { title?: string } }) => (
    <div data-testid="schema-viewer">{schema.title}</div>
  ),
}));

const DOCUMENTS: AdapterSchemaDocument[] = [
  {
    id: "anthropic/api", kind: "execution",
    provider: "anthropic",
    mode: "api",
    title: "Anthropic API",
    description: "Anthropic Messages API options.",
    schema: { type: "object", title: "AnthropicAPIOptions" },
  },
  {
    id: "openai/api", kind: "execution",
    provider: "openai",
    mode: "api",
    title: "OpenAI API",
    description: "OpenAI Responses API options.",
    schema: { type: "object", title: "OpenAIAPIOptions" },
  },
  {
    id: "openai/cli", kind: "execution",
    provider: "openai",
    mode: "cli",
    title: "OpenAI CLI",
    description: "Codex CLI options.",
    schema: { type: "object", title: "OpenAICLIOptions" },
  },
];

describe("AdapterSchemasPage", () => {
  afterEach(cleanup);
  it("renders the selected schema and keeps provider and mode choices in the route", async () => {
    const navigate = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      json: async () => DOCUMENTS,
    }));

    renderPage(
      <AdapterSchemasPage
        selection={{ provider: "openai", mode: "cli" }}
        onNavigate={navigate}
      />,
    );

    expect(await screen.findByRole("heading", { name: "OpenAI CLI" })).toBeInTheDocument();
    expect(screen.getByTestId("schema-viewer")).toHaveTextContent("OpenAICLIOptions");

    fireEvent.change(screen.getByLabelText("Resource"), { target: { value: "openai/api" } });
    expect(navigate).toHaveBeenCalledWith("/adapter-schemas/openai/api");

    fireEvent.change(screen.getByLabelText("Provider"), { target: { value: "anthropic" } });
    expect(navigate).toHaveBeenCalledWith("/adapter-schemas/anthropic/api");
  });

  it("retains separate resource drafts and query state through navigation", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, json: async () => DOCUMENTS }));
    function Workspace() {
      const [location, navigate] = useState("/adapter-schemas/openai/api?view=json&q=API");
      const url = new URL(location, "http://localhost");
      return <AdapterSchemasPage selection={parseAdapterSchemaPath(url.pathname)} search={url.search} onNavigate={navigate} />;
    }
    renderPage(<Workspace />);
    const input = await screen.findByRole("textbox", { name: "JSON value" });
    fireEvent.change(input, { target: { value: '{"model":"example-model"}' } });
    fireEvent.change(screen.getByLabelText("Resource"), { target: { value: "openai/cli" } });
    expect(screen.getByRole("textbox", { name: "JSON value" })).toHaveValue("{}");
    expect(screen.getByLabelText("Search resources")).toHaveValue("API");
    fireEvent.change(screen.getByLabelText("Resource"), { target: { value: "openai/api" } });
    expect(screen.getByRole("textbox", { name: "JSON value" })).toHaveValue('{"model":"example-model"}');
    fireEvent.change(screen.getByRole("textbox", { name: "JSON value" }), { target: { value: "{" } });
    expect(screen.getByRole("button", { name: "Validate schema" })).toBeDisabled();
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });
});

function renderPage(children: PropsWithChildren["children"]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{children}</QueryClientProvider>);
}
