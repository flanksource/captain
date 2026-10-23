package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

func inventoryCLIOptions() {
	flagLine := regexp.MustCompile(`^ {2,6}(?:-[a-zA-Z],\s+)?--[a-zA-Z]`)
	longFlag := regexp.MustCompile(`--[a-zA-Z][a-zA-Z0-9-]*`)
	for _, provider := range []struct{ name, help, version string }{
		{"anthropic", "claude", "claude-cli 2.1.309"},
		{"openai", "codex", "codex-cli 0.164.0"},
		{"google", "gemini", "gemini-cli 0.64.0"},
	} {
		raw, err := os.ReadFile(".tmp/adapter-schema-sources/" + provider.help + "-help.txt")
		must(err)
		path := "pkg/adapters/" + provider.name + "/cli.schema.json"
		schema := readObject(path)
		mapped := map[string]map[string]any{}
		for _, raw := range schema["properties"].(map[string]any) {
			field := raw.(map[string]any)
			option := field["x-captain-option"].(map[string]any)
			mapped[option["nativeName"].(string)] = option
		}
		var flags, missing []string
		for _, line := range strings.Split(string(raw), "\n") {
			if !flagLine.MatchString(line) {
				continue
			}
			header := strings.SplitN(strings.TrimSpace(line), "  ", 2)[0]
			aliases := longFlag.FindAllString(header, -1)
			var option map[string]any
			for _, name := range aliases {
				if mapped[name] != nil {
					option = mapped[name]
					break
				}
			}
			if option == nil {
				missing = append(missing, aliases[0])
				continue
			}
			if len(aliases) > 1 {
				option["aliases"] = aliases
			}
			flags = append(flags, aliases...)
		}
		if len(missing) > 0 {
			panic(fmt.Sprintf("%s CLI flags missing: %v", provider.name, missing))
		}
		sort.Strings(flags)
		schema["x-captain-inventory"] = map[string]any{"version": provider.version, "flags": flags}
		writeObject(path, schema)
	}
}
