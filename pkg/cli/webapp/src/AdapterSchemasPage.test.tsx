import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import type { PropsWithChildren } from "react";
import { describe, expect, it, vi } from "vitest";
import { AdapterSchemasPage } from "./AdapterSchemasPage";
import type { AdapterSchemaDocument } from "./adapterSchemas";

vi.mock("@flanksource/clicky-ui/data", () => ({
  SchemaViewer: ({ schema }: { schema: { title?: string } }) => (
    <div data-testid="schema-viewer">{schema.title}</div>
  ),
}));

const DOCUMENTS: AdapterSchemaDocument[] = [
  {
    provider: "anthropic",
    mode: "api",
    title: "Anthropic API",
    description: "Anthropic Messages API options.",
    schema: { type: "object", title: "AnthropicAPIOptions" },
  },
  {
    provider: "openai",
    mode: "api",
    title: "OpenAI API",
    description: "OpenAI Responses API options.",
    schema: { type: "object", title: "OpenAIAPIOptions" },
  },
  {
    provider: "openai",
    mode: "cli",
    title: "OpenAI CLI",
    description: "Codex CLI options.",
    schema: { type: "object", title: "OpenAICLIOptions" },
  },
];

describe("AdapterSchemasPage", () => {
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

    fireEvent.click(screen.getByRole("radio", { name: "API" }));
    expect(navigate).toHaveBeenCalledWith("/adapter-schemas/openai/api");

    fireEvent.change(screen.getByLabelText("Provider"), { target: { value: "anthropic" } });
    expect(navigate).toHaveBeenCalledWith("/adapter-schemas/anthropic/api");
  });
});

function renderPage(children: PropsWithChildren["children"]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}>{children}</QueryClientProvider>);
}
