# Captain resource schemas

`captain-backends.json` describes Captain's own sandbox backends and is embedded in the binary.

Native provider resource schemas (OpenAI, Anthropic, Google) are not committed or embedded. The `Adapter schemas` workflow generates them with `go run ./hack/adapterresources resources` and publishes them to the `schemas` branch, where the web UI loads `adapters/native-resources.json`. See the [adapter resource guide](../../../docs/adapter-resource-schemas.md).
