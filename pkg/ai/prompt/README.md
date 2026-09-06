# Rendering prompt specifications

`Template.Render` takes one `RenderOptions` value. `Data` supplies template variables. `Output` supplies a Go structured-output target; when omitted, the template's output schema is retained as `Spec.Prompt.SchemaJSON`.

Set `Declared: true` when the rendered prompt will participate in configuration composition. This preserves the authored model selector and runtime axes while rendering the body, source, native frontmatter, dotprompt configuration, and output schema. It does not choose a provider or add model defaults. Resolve the resulting specification with the other configuration layers before execution.

```go
template := prompt.Load("---\nmodel: agent:sonnet\nconfig:\n  temperature: 0\n---\nReview {{target}}.\n")
spec, config, err := template.Render(prompt.RenderOptions{
    Data: map[string]any{"target": "parser.go"},
    Declared: true,
})
```

`spec` and `config` carry the same projected model and budget values. Dotprompt `config.temperature`, `config.reasoning`, and `config.maxOutputTokens` override their native frontmatter equivalents. Explicit zero values keep their presence metadata for later composition. Omit `Declared` when the caller wants the existing immediate model resolution behavior.

`Library.Render` accepts a name and the same options: `library.Render("review.prompt", options)`.

Named model calls now use `ai.PromptRequest{Name: "review", Spec: resolvedSpec}`. `Agent.ExecutePrompt` forwards that complete specification directly to the provider. Put user/system text, source, JSON Schema, schema strictness, and native Go schema targets under `Spec.Prompt`. Other runtime groups, session state, and explicit-value metadata stay on `Spec`. Batch responses remain keyed by `Name`; cost accounting and terminal outcomes are unchanged.

Migration: replace `template.Render(data, output)` with `template.Render(prompt.RenderOptions{Data: data, Output: output})`, and replace flat named-request prompt/schema fields with the corresponding `Spec.Prompt` fields. Callers composing layers should also set `Declared: true` and must not overwrite the rendered model from a second frontmatter parse.

Run the focused rendering and transport examples/tests from the Captain checkout:

```sh
go test ./pkg/ai/prompt -run '^TestPromptDocuments$' -ginkgo.focus='Declared prompt rendering' -count=1
go test ./pkg/ai -run '^TestAttachmentCapabilities$' -ginkgo.focus='Named prompt Spec transport' -count=1
```
