package genkit

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DeepSeek image transport", func() {
	DescribeTable("sends ordered text and images to chat completions", func(conversation, streaming bool) {
		requests := make(chan map[string]any, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			Expect(r.Method).To(Equal(http.MethodPost))
			Expect(r.URL.Path).To(Equal("/chat/completions"))
			var body map[string]any
			Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
			requests <- body
			if streaming {
				w.Header().Set("Content-Type", "text/event-stream")
				_, err := io.WriteString(w, "data: {\"id\":\"example\",\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"A chart.\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"example\",\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-flash\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				Expect(err).NotTo(HaveOccurred())
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, err := io.WriteString(w, `{"id":"example","object":"chat.completion","model":"deepseek-flash","choices":[{"index":0,"message":{"role":"assistant","content":"A chart."},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`)
			Expect(err).NotTo(HaveOccurred())
		}))
		DeferCleanup(server.Close)
		model, err := api.ResolveModel(api.Model{Name: "deepseek-flash", Mode: api.ModeAPI})
		Expect(err).NotTo(HaveOccurred())
		provider, err := New(ai.Config{Model: model, APIKey: "example-key", APIURL: server.URL})
		Expect(err).NotTo(HaveOccurred())
		first := api.AttachmentRef{ID: api.AttachmentIDPrefix + strings.Repeat("a", 64), MediaType: "image/png"}.
			WithPreparedContent(api.AttachmentContent{Bytes: []byte("first")})
		second := api.AttachmentRef{ID: api.AttachmentIDPrefix + strings.Repeat("b", 64), MediaType: "image/jpeg"}.
			WithPreparedContent(api.AttachmentContent{Bytes: []byte("second")})
		request := ai.Request{Model: model, Prompt: api.Prompt{User: "Compare charts.", Attachments: []api.AttachmentRef{first, second}}}
		if conversation {
			request.Prompt = api.Prompt{}
			request.Messages = []api.Message{{Role: api.RoleUser, Parts: []api.Part{
				{Type: api.PartText, Text: "Compare charts."},
				{Type: api.PartAttachment, Attachment: &first},
				{Type: api.PartAttachment, Attachment: &second},
			}}}
		}
		if streaming {
			events, err := provider.ExecuteStream(context.Background(), request)
			Expect(err).NotTo(HaveOccurred())
			var text string
			var complete bool
			for event := range events {
				Expect(event.Kind).NotTo(Equal(ai.EventError), event.Error)
				if event.Kind == ai.EventText {
					text += event.Text
				}
				complete = complete || event.Kind == ai.EventResult
			}
			Expect(text).To(Equal("A chart."))
			Expect(complete).To(BeTrue())
		} else {
			response, err := provider.Execute(context.Background(), request)
			Expect(err).NotTo(HaveOccurred())
			Expect(response.Text).To(Equal("A chart."))
		}
		var body map[string]any
		Expect(requests).To(Receive(&body))
		Expect(body["model"]).To(Equal(model.Name))
		if streaming {
			Expect(body).To(HaveKeyWithValue("stream", true))
		} else {
			Expect(body).NotTo(HaveKey("stream"))
		}
		Expect(body["messages"]).To(Equal([]any{map[string]any{
			"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Compare charts."},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,Zmlyc3Q="}},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64,c2Vjb25k"}},
			},
		}}))
	},
		Entry("buffered prompt", false, false),
		Entry("streaming prompt", false, true),
		Entry("buffered conversation", true, false),
		Entry("streaming conversation", true, true),
	)

	It("rejects unresolved Flash images before sending a request", func() {
		model, err := api.ResolveModel(api.Model{Name: "deepseek-flash", Mode: api.ModeAPI})
		Expect(err).NotTo(HaveOccurred())
		provider := &Provider{provider: ai.DeepSeek, cfg: ai.Config{Model: model}, modelRef: "deepseek/deepseek-flash"}
		_, err = provider.Execute(context.Background(), ai.Request{Model: model, Prompt: api.Prompt{
			Attachments: []api.AttachmentRef{{Path: "chart.png", MediaType: "image/png"}},
		}})
		Expect(err).To(MatchError(ContainSubstring("is not prepared")))
	})
})
