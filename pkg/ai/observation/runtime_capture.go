package observation

import "context"

type runtimeCaptureContextKey struct{}

// RuntimeCaptureConfig carries observation-only process routing. Values stay
// out of the serializable request and ordinary prompt execution.
type RuntimeCaptureConfig struct {
	MCPConfigs  []string
	Environment map[string]string
}

// ContextWithRuntimeCapture attaches process-routing inputs for provider seams
// that Captain can intercept during an observation run.
func ContextWithRuntimeCapture(ctx context.Context, config RuntimeCaptureConfig) context.Context {
	copy := RuntimeCaptureConfig{
		MCPConfigs:  append([]string(nil), config.MCPConfigs...),
		Environment: make(map[string]string, len(config.Environment)),
	}
	for name, value := range config.Environment {
		copy.Environment[name] = value
	}
	return context.WithValue(ctx, runtimeCaptureContextKey{}, copy)
}

// RuntimeCaptureFromContext returns a detached copy of observation-only
// process routing, or an empty config during ordinary prompt execution.
func RuntimeCaptureFromContext(ctx context.Context) RuntimeCaptureConfig {
	if ctx == nil {
		return RuntimeCaptureConfig{}
	}
	config, _ := ctx.Value(runtimeCaptureContextKey{}).(RuntimeCaptureConfig)
	copy := RuntimeCaptureConfig{
		MCPConfigs:  append([]string(nil), config.MCPConfigs...),
		Environment: make(map[string]string, len(config.Environment)),
	}
	for name, value := range config.Environment {
		copy.Environment[name] = value
	}
	return copy
}
