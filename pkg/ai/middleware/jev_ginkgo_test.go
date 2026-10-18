package middleware

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestJevConversion(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Jev Conversion Suite")
}

type jevRecordingProvider struct {
	request ai.Request
}

func (p *jevRecordingProvider) GetModel() string       { return "jev-latest" }
func (p *jevRecordingProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Jev, ai.ModeAPI) }
func (p *jevRecordingProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	p.request = req
	return &ai.Response{Text: `{"model":"jev-1.14.0","answers":{"route":{"type":"choice","choice":"billing","confidence":0.9,"probabilities":{"billing":0.9,"technical":0.1}},"valid":{"type":"noul","noul":0.8},"risk":{"type":"score","score":1.25,"confidence":0.7,"legend":{"0":"Low","1":"Medium","2":"High"},"probabilities":{"0":0,"1":0.75,"2":0.25}}},"usage":{"input_tokens":12,"output_tokens":3}}`}, nil
}

func (p *jevRecordingProvider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	response, err := p.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	events := make(chan ai.Event, 2)
	events <- ai.Event{Kind: ai.EventText, Text: response.Text}
	events <- ai.Event{Kind: ai.EventResult, Success: true, StructuredData: json.RawMessage(response.Text)}
	close(events)
	return events, nil
}

var _ = Describe("Jev conversion", func() {
	const schema = `{"type":"object","additionalProperties":false,"required":["route","valid","risk"],"properties":{"route":{"type":"string","enum":["billing","technical"]},"valid":{"type":"number","minimum":0,"maximum":1},"risk":{"type":"number","minimum":0,"maximum":2,"x-typesafe-criteria":["Low","Medium","High"]}}}`
	It("logs a native request downstream and returns projected plus native answers", func() {
		inner := &jevRecordingProvider{}
		wrapped, err := WithJevConversion()(inner)
		Expect(err).NotTo(HaveOccurred())
		response, err := wrapped.Execute(context.Background(), ai.Request{Prompt: api.Prompt{User: "Review the commit", SchemaJSON: json.RawMessage(schema)}})
		Expect(err).NotTo(HaveOccurred())
		var native map[string]any
		Expect(json.Unmarshal([]byte(inner.request.Prompt.User), &native)).To(Succeed())
		Expect(native).To(HaveKeyWithValue("state", "Review the commit"))
		Expect(native["questions"]).To(HaveLen(3))
		Expect(response.Text).To(MatchJSON(`{"value":{"route":"billing","valid":0.8,"risk":1.25},"typesafe":{"model":"jev-1.14.0","answers":{"route":{"type":"choice","choice":"billing","confidence":0.9,"probabilities":{"billing":0.9,"technical":0.1}},"valid":{"type":"noul","noul":0.8},"risk":{"type":"score","score":1.25,"confidence":0.7,"legend":{"0":"Low","1":"Medium","2":"High"},"probabilities":{"0":0,"1":0.75,"2":0.25}}},"usage":{"input_tokens":12,"output_tokens":3}}}`))
	})
	It("validates the projected value under strict JSON Schema validation", func() {
		inner := &jevRecordingProvider{}
		wrapped, err := Wrap(inner, WithJevConversion(), WithSchemaValidation())
		Expect(err).NotTo(HaveOccurred())
		response, err := wrapped.Execute(context.Background(), ai.Request{Prompt: api.Prompt{
			User: "Review the commit", SchemaJSON: json.RawMessage(schema), SchemaStrictness: api.SchemaStrictnessError,
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StructuredData).To(MatchJSON(response.Text))
	})
	It("accepts authored native TypeSafe questions", func() {
		inner := &jevRecordingProvider{}
		wrapped, err := WithJevConversion()(inner)
		Expect(err).NotTo(HaveOccurred())
		questions := json.RawMessage(`{
			"route":{"type":"choice","instructions":"Which route?","criteria":{"billing":null,"technical":null}},
			"valid":{"type":"noul","instructions":"Is the input valid?"},
			"risk":{"type":"score","instructions":"How risky?","criteria":["Low","Medium","High"]}
		}`)
		response, err := wrapped.Execute(context.Background(), ai.Request{Prompt: api.Prompt{
			User: "Review the commit", OutputSchemaType: api.SchemaTypeTypeSafe, TypeSafeQuestions: questions,
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Text).To(ContainSubstring(`"value":{"risk":1.25,"route":"billing","valid":0.8}`))
		var sent struct {
			Questions map[string]any `json:"questions"`
		}
		Expect(json.Unmarshal([]byte(inner.request.Prompt.User), &sent)).To(Succeed())
		Expect(sent.Questions).To(HaveLen(3))
	})
	It("streams through the cache wrapper with the native request", func() {
		inner := &jevRecordingProvider{}
		wrapped, err := Wrap(inner, WithCacheInstance(nil), WithJevConversion())
		Expect(err).NotTo(HaveOccurred())
		events, err := wrapped.(ai.StreamingProvider).ExecuteStream(context.Background(), ai.Request{Prompt: api.Prompt{
			User: "Review the commit", SchemaJSON: json.RawMessage(schema),
		}})
		Expect(err).NotTo(HaveOccurred())
		var received []ai.Event
		for event := range events {
			received = append(received, event)
		}
		Expect(received).To(HaveLen(2))
		Expect(received[0].Text).To(MatchJSON(`{"value":{"route":"billing","valid":0.8,"risk":1.25},"typesafe":{"model":"jev-1.14.0","answers":{"route":{"type":"choice","choice":"billing","confidence":0.9,"probabilities":{"billing":0.9,"technical":0.1}},"valid":{"type":"noul","noul":0.8},"risk":{"type":"score","score":1.25,"confidence":0.7,"legend":{"0":"Low","1":"Medium","2":"High"},"probabilities":{"0":0,"1":0.75,"2":0.25}}},"usage":{"input_tokens":12,"output_tokens":3}}}`))
		Expect(received[1].StructuredData).To(MatchJSON(received[0].Text))
		Expect(inner.request.Prompt.User).To(ContainSubstring(`"questions"`))
	})
})
