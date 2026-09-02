package runtimeprofiles

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Record ids", func() {
	It("round-trips kind, source and key and stays URL-safe", func() {
		id := EncodeID(KindPreset, "a1b2c3d4e5f6", "team.review_v2-final")
		Expect(strings.ContainsAny(id, "/+=")).To(BeFalse(), id)
		Expect(DecodeID(id)).To(Equal(RecordRef{Kind: KindPreset, SourceID: "a1b2c3d4e5f6", Key: "team.review_v2-final"}))
		Expect(LooksLikeID(id)).To(BeTrue())

		profileID := EncodeID(KindProfile, DBSourceID, "0f9b3a3e-4d4c-4d7c-9a3e-2b7e8c1d2f10")
		Expect(DecodeID(profileID)).To(Equal(RecordRef{
			Kind: KindProfile, SourceID: DBSourceID, Key: "0f9b3a3e-4d4c-4d7c-9a3e-2b7e8c1d2f10",
		}))
	})

	It("treats names, uuids and malformed strings as not-an-id", func() {
		for _, ref := range []string{
			"Review", "review", "review-1", "personal guardrails", "",
			"0f9b3a3e-4d4c-4d7c-9a3e-2b7e8c1d2f10", "not an id!",
		} {
			Expect(LooksLikeID(ref)).To(BeFalse(), ref)
			_, err := DecodeID(ref)
			Expect(err).To(HaveOccurred(), ref)
		}
	})

	It("rejects ids missing a part or naming an unknown kind", func() {
		_, err := DecodeID(EncodeID(KindPreset, "db", ""))
		Expect(err).To(MatchError(ContainSubstring("invalid runtime record id")))
		_, err = DecodeID(EncodeID("", "db", "key"))
		Expect(err).To(MatchError(ContainSubstring("invalid runtime record id")))
		_, err = DecodeID(EncodeID(Kind("prompt"), "db", "key"))
		Expect(err).To(MatchError(ContainSubstring(`unknown kind "prompt"`)))
		Expect(LooksLikeID(EncodeID(Kind("prompt"), "db", "key"))).To(BeFalse())
	})

	It("keeps ids of the two kinds distinct for the same source and key", func() {
		Expect(EncodeID(KindPreset, "db", "k")).NotTo(Equal(EncodeID(KindProfile, "db", "k")))
		Expect(strings.HasPrefix(EncodeID(KindPreset, "db", "k"), EncodeID(KindPreset, "db", ""))).To(BeFalse())
	})
})
