package adapters

//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_anthropic_api.go anthropic/api.schema.json
//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_anthropic_agent.go anthropic/agent.schema.json
//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_anthropic_cli.go anthropic/cli.schema.json
//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_openai_api.go openai/api.schema.json
//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_openai_agent.go openai/agent.schema.json
//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_openai_cli.go openai/cli.schema.json
//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_google_api.go google/api.schema.json
//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_google_cli.go google/cli.schema.json
//go:generate go tool go-jsonschema --only-models --struct-name-from-title --extra-imports --package adapters --tags json --output zz_generated_deepseek_api.go deepseek/api.schema.json
