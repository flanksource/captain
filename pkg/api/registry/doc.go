// Package registry owns captain's model identity: the provider descriptors, the
// Effort/RuntimeMode enums, the Model spec type, the compact model grammar, and
// the embedded model catalog.
//
// A runtime is a (provider, mode) pair. The provider is derived from the model
// name via the claim table; the mode — api | agent | cli | cmux — is the only
// part a caller authors. There is no composite adapter id: captain used to
// compress the pair into one frozen string ("claude-agent", "anthropic"), and
// that string meant the adapter on the way out and the mode on the way in, so
// echoing a response back as a request silently named a different runtime.
//
// It is a leaf — it imports nothing else from captain. That is deliberate:
// pkg/api decodes specs (and therefore parses model strings) during
// unmarshalling, so the parser cannot live above pkg/api without a cycle, and
// splitting the knowledge across both packages is exactly what let the compact
// grammar and the selector grammar drift apart. pkg/api re-exports everything
// here via aliases, so api.Model and api.RuntimeMode remain the names callers use.
package registry
