package adapters

//go:generate go tool go-jsonschema --only-models --schema-root-type =AnthropicAPIOptions --extra-imports --package adapters --tags json --output zz_generated_anthropic_api.go anthropic/api.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =AnthropicAgentOptions --extra-imports --package adapters --tags json --output zz_generated_anthropic_agent.go anthropic/agent.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =AnthropicCLIOptions --extra-imports --package adapters --tags json --output zz_generated_anthropic_cli.go anthropic/cli.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =OpenAIAPIOptions --extra-imports --package adapters --tags json --output zz_generated_openai_api.go openai/api.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =OpenAIAgentOptions --extra-imports --package adapters --tags json --output zz_generated_openai_agent.go openai/agent.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =OpenAICLIOptions --extra-imports --package adapters --tags json --output zz_generated_openai_cli.go openai/cli.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =GoogleAPIOptions --extra-imports --package adapters --tags json --output zz_generated_google_api.go google/api.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =GoogleCLIOptions --extra-imports --package adapters --tags json --output zz_generated_google_cli.go google/cli.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =DeepSeekAPIOptions --extra-imports --package adapters --tags json --output zz_generated_deepseek_api.go deepseek/api.schema.json
//go:generate go tool go-jsonschema --only-models --schema-root-type =JevAPIOptions --extra-imports --package adapters --tags json --output zz_generated_jev_api.go jev/api.schema.json
//go:generate go run ../../hack/adapterdto
