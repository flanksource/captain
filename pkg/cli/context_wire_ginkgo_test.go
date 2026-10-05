package cli

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Session context wire", func() {
	It("serializes reported zero occupancy as a known snapshot", func() {
		encoded, err := json.Marshal(SessionContextWire{WindowTokens: 258400, FreePercent: 100})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(MatchJSON(`{"usedTokens":0,"windowTokens":258400,"freePercent":100}`))
	})
})
