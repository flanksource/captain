package cli

import (
	"github.com/flanksource/commons-db/otel"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("telemetryConfig", func() {
	const (
		flagURL = "http://flag-collector:4318"
		envURL  = "http://env-collector:4318"
	)
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}

	DescribeTable("resolves the tracer from the flag and OTEL_* environment",
		func(flag string, vars map[string]string, want otel.Tracer, wantOK bool) {
			tracer, ok, err := telemetryConfig(flag, env(vars))
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{tracer, ok}).To(Equal([]any{want, wantOK}))
		},
		Entry("off when neither flag nor env is set", "", nil, otel.Tracer{}, false),
		Entry("env endpoint with the default service name", "",
			map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": envURL},
			otel.Tracer{ServiceName: "captain", CollectorURL: envURL}, true),
		Entry("flag overrides the env endpoint", flagURL,
			map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": envURL},
			otel.Tracer{ServiceName: "captain", CollectorURL: flagURL}, true),
		Entry("service name and insecure from env", "collector:4317",
			map[string]string{"OTEL_SERVICE_NAME": "gavel", "OTEL_EXPORTER_OTLP_INSECURE": "true"},
			otel.Tracer{ServiceName: "gavel", CollectorURL: "collector:4317", Insecure: true}, true),
	)

	It("rejects a malformed OTEL_EXPORTER_OTLP_INSECURE", func() {
		_, _, err := telemetryConfig(flagURL, env(map[string]string{"OTEL_EXPORTER_OTLP_INSECURE": "maybe"}))
		Expect(err).To(MatchError(ContainSubstring("OTEL_EXPORTER_OTLP_INSECURE")))
	})
})
