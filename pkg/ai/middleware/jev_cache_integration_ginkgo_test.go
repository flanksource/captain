package middleware

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/cache"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Jev native cache integration", func() {
	It("keys and stores the native request and response before projection", func() {
		store, err := cache.New(cache.Config{DBPath: filepath.Join(GinkgoT().TempDir(), "jev-cache.db"), TTL: time.Hour})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(store.Close()).To(Succeed()) })

		inner := &jevRecordingProvider{}
		wrapped, err := Wrap(inner, WithCacheInstance(store), WithJevConversion())
		Expect(err).NotTo(HaveOccurred())
		request := ai.Request{Prompt: api.Prompt{User: "Review the commit", SchemaJSON: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["route","valid","risk"],"properties":{"route":{"type":"string","enum":["billing","technical"]},"valid":{"type":"number","minimum":0,"maximum":1},"risk":{"type":"number","minimum":0,"maximum":2,"x-typesafe-criteria":["Low","Medium","High"]}}}`)}}

		first, err := wrapped.Execute(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		entry, err := store.Get(inner.request.Prompt.CacheIdentity(), inner.GetModel())
		Expect(err).NotTo(HaveOccurred())
		var cached map[string]any
		Expect(json.Unmarshal([]byte(entry.Response), &cached)).To(Succeed())
		Expect(cached).To(HaveKey("answers"))
		Expect(cached).NotTo(HaveKey("value"))

		second, err := wrapped.Execute(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(second.CacheHit).To(BeTrue())
		Expect(second.Text).To(MatchJSON(first.Text))
	})
})
