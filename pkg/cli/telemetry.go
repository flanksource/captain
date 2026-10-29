package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/flanksource/commons-db/otel"
	"github.com/spf13/pflag"
)

const otelCollectorFlag = "otel-collector"

var (
	otelCollector     string
	telemetryShutdown func(context.Context) error
)

// BindTelemetryFlags binds --otel-collector, which overrides the standard
// OTEL_EXPORTER_OTLP_ENDPOINT.
func BindTelemetryFlags(flags *pflag.FlagSet) {
	flags.StringVar(&otelCollector, otelCollectorFlag, "",
		"OpenTelemetry collector to export run traces to: http(s)://host:4318 for OTLP/HTTP or host:4317 for OTLP/gRPC (default: $OTEL_EXPORTER_OTLP_ENDPOINT; tracing is off when neither is set)")
}

// telemetryConfig resolves the tracer from the flag and the standard OTEL_*
// environment. ok is false when no collector is configured.
func telemetryConfig(flag string, getenv func(string) string) (tracer otel.Tracer, ok bool, err error) {
	url := flag
	if url == "" {
		url = getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if url == "" {
		return otel.Tracer{}, false, nil
	}
	service := getenv("OTEL_SERVICE_NAME")
	if service == "" {
		service = "captain"
	}
	var insecure bool
	if v := getenv("OTEL_EXPORTER_OTLP_INSECURE"); v != "" {
		if insecure, err = strconv.ParseBool(v); err != nil {
			return otel.Tracer{}, false, fmt.Errorf("OTEL_EXPORTER_OTLP_INSECURE=%q: %w", v, err)
		}
	}
	return otel.Tracer{ServiceName: service, CollectorURL: url, Insecure: insecure}, true, nil
}

// InitTelemetry installs the global OpenTelemetry tracer provider when a
// collector is configured, so run, model-call, tool and database spans are
// exported. cmd/captain wires it from PersistentPreRun.
func InitTelemetry(ctx context.Context) error {
	tracer, ok, err := telemetryConfig(otelCollector, os.Getenv)
	if err != nil || !ok {
		return err
	}
	telemetryShutdown, err = tracer.Start(ctx)
	return err
}

// FlushTelemetry exports buffered spans and closes the exporter. cmd/captain
// calls it after Execute returns, including on the error path.
func FlushTelemetry() {
	if telemetryShutdown == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := telemetryShutdown(ctx); err != nil {
		log.Errorf("opentelemetry: %v", err)
	}
}
