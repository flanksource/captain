package adapters

import "strings"

func pruneDefinitions(schema map[string]any) {
	for _, keyword := range []string{"definitions", "$defs"} {
		definitions, ok := schema[keyword].(map[string]any)
		if !ok {
			continue
		}
		used := map[string]any{}
		var walk func(any)
		walk = func(raw any) {
			switch node := raw.(type) {
			case map[string]any:
				if ref, ok := node["$ref"].(string); ok && strings.HasPrefix(ref, "#/"+keyword+"/") {
					key := strings.SplitN(strings.TrimPrefix(ref, "#/"+keyword+"/"), "/", 2)[0]
					key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
					if _, exists := used[key]; !exists {
						if definition, exists := definitions[key]; exists {
							used[key] = definition
							walk(definition)
						}
					}
				}
				for key, value := range node {
					if key != "definitions" && key != "$defs" {
						walk(value)
					}
				}
			case []any:
				for _, value := range node {
					walk(value)
				}
			}
		}
		walk(schema)
		if len(used) == 0 {
			delete(schema, keyword)
		} else {
			schema[keyword] = used
		}
	}
}
