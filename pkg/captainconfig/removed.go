package captainconfig

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// removedKey names a key captain no longer reads, and says what to write
// instead. It is a rejection table, not a translation table: the file is never
// rewritten on the user's behalf, because guessing what a removed key meant is
// how a runtime silently changes under someone who never asked for it.
type removedKey struct {
	// path is the dotted location in the file, with "*" for a map key.
	path string
	// why is the one-line reason the key no longer exists.
	why string
	// instead is the replacement, indented under the message.
	instead string
}

// removedKeys are the keys deleted when a runtime became (model, mode).
//
// Two generations are represented. The older one is the flat global selection
// (ai.model / ai.reasoningEffort), which moved under ai.providers.<name>. The
// newer one is the composite adapter vocabulary — "claude-agent", "anthropic"
// and their siblings — which named the adapter outbound and the mode inbound,
// so the same token meant two different things depending on direction.
var removedKeys = []removedKey{
	{
		path:    "ai.backend",
		why:     "a runtime is (model, mode); the composite adapter ids are gone",
		instead: "ai.providers.<provider>.mode: api | agent | cli | cmux",
	},
	{
		path:    "ai.providers.*.agent",
		why:     "the per-provider adapter id was the mode wearing another name",
		instead: "ai.providers.<provider>.mode: api | agent | cli | cmux",
	},
	{
		path:    "ai.disabled.backends",
		why:     "a disabled runtime is a (provider, mode) pair, not one token",
		instead: "ai.disabled.runtimes: [{provider: anthropic, mode: cmux}]",
	},
	{
		path:    "ai.model",
		why:     "model defaults are per provider, so one file can hold several",
		instead: "ai.providers.<provider>.model: <model>",
	},
	{
		path:    "ai.reasoningEffort",
		why:     "effort defaults are per provider, alongside the model they apply to",
		instead: "ai.providers.<provider>.reasoningEffort: <effort>",
	},
	{
		path:    "prompts.schemaRepair.backend",
		why:     "a runtime is (model, mode); the composite adapter ids are gone",
		instead: "prompts.schemaRepair.mode: api | agent | cli | cmux",
	},
}

// checkRemovedKeys reports every removed key present in a config file, as one
// error naming all of them. Reporting them together matters: a file written
// before the rename usually carries several, and fixing them one error at a time
// is a bad way to spend an afternoon.
func checkRemovedKeys(path string, data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		// Malformed YAML is the decoder's error to report, not ours.
		return nil
	}
	found := collectRemoved(&root)
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)

	var b strings.Builder
	fmt.Fprintf(&b, "%s uses %s captain no longer reads:", path, plural(len(found), "a key", "keys"))
	for _, name := range found {
		key := removedKeyFor(name)
		fmt.Fprintf(&b, "\n\n  %s — %s\n    use instead:\n      %s", name, key.why, key.instead)
	}
	b.WriteString("\n\nEdit the file, or run `captain configure` to rewrite these settings.")
	return fmt.Errorf("%s", b.String())
}

func removedKeyFor(path string) removedKey {
	for _, key := range removedKeys {
		if key.path == path {
			return key
		}
	}
	return removedKey{path: path}
}

// collectRemoved walks the document once, matching each removed key's path
// against the node tree. "*" matches any mapping key, which is how the
// per-provider `agent:` key is found without knowing the provider names.
func collectRemoved(root *yaml.Node) []string {
	var found []string
	for _, key := range removedKeys {
		if nodeHasPath(root, strings.Split(key.path, ".")) {
			found = append(found, key.path)
		}
	}
	return found
}

func nodeHasPath(node *yaml.Node, path []string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.DocumentNode {
		for _, child := range node.Content {
			if nodeHasPath(child, path) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return true
	}
	if node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, value := node.Content[i], node.Content[i+1]
		if path[0] != "*" && name.Value != path[0] {
			continue
		}
		if len(path) == 1 {
			// An empty value is the key still being present but unset, which is
			// how a round-tripped file looks; it is not a setting to reject.
			if value.Tag == "!!null" {
				continue
			}
			return true
		}
		if nodeHasPath(value, path[1:]) {
			return true
		}
	}
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
