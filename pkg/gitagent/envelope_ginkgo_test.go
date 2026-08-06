package gitagent_test

import (
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
)

const testOID = "0123456789abcdef0123456789abcdef01234567"

func validEnvelope() gitagent.Envelope {
	return gitagent.Envelope{
		Version: gitagent.ProtocolVersion,
		Task:    "my-task",
		Attempt: 2,
		Base:    testOID,
		Depth:   0,
	}
}

var _ = Describe("envelope encode/decode", func() {
	It("round-trips a dispatch envelope", func() {
		e := validEnvelope()
		e.Agent = "worker-01"
		e.Relay = gitagent.RelaySync
		e.Mailbox = "mailboxes/" + strings.Repeat("a", 64) + ".git"
		opts, err := e.Encode()
		Expect(err).NotTo(HaveOccurred())
		Expect(opts[0]).To(Equal(gitagent.EnvelopeVersionTag))
		decoded, err := gitagent.DecodeEnvelope(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(decoded).To(Equal(e))
	})

	It("round-trips a minimal envelope", func() {
		e := validEnvelope()
		opts, err := e.Encode()
		Expect(err).NotTo(HaveOccurred())
		decoded, err := gitagent.DecodeEnvelope(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(decoded).To(Equal(e))
	})

	It("rejects a missing envelope", func() {
		_, err := gitagent.DecodeEnvelope(nil)
		Expect(err).To(HaveOccurred())
	})

	It("rejects an unknown version tag (R4.1)", func() {
		opts, _ := validEnvelope().Encode()
		opts[0] = "captain-envelope-v2"
		_, err := gitagent.DecodeEnvelope(opts)
		Expect(err).To(MatchError(ContainSubstring("version tag")))
	})

	It("rejects unknown keys, repeats, and non key=value options", func() {
		base, _ := validEnvelope().Encode()
		for _, extra := range []string{"evil=1", "task=my-task", "notkeyvalue", "task="} {
			opts := append(append([]string{}, base...), extra)
			_, err := gitagent.DecodeEnvelope(opts)
			Expect(err).To(HaveOccurred(), "option %q", extra)
		}
	})

	It("rejects a missing required key", func() {
		for _, drop := range []string{"task=", "attempt=", "base=", "depth="} {
			opts, _ := validEnvelope().Encode()
			kept := opts[:0:0]
			for _, o := range opts {
				if !strings.HasPrefix(o, drop) {
					kept = append(kept, o)
				}
			}
			_, err := gitagent.DecodeEnvelope(kept)
			Expect(err).To(HaveOccurred(), "dropped %q", drop)
		}
	})

	It("rejects invalid field values", func() {
		bad := []gitagent.Envelope{}
		e := validEnvelope()
		e.Task = "Bad Task"
		bad = append(bad, e)
		e = validEnvelope()
		e.Attempt = 0
		bad = append(bad, e)
		e = validEnvelope()
		e.Base = "not-an-oid"
		bad = append(bad, e)
		e = validEnvelope()
		e.Depth = gitagent.MaxHookDepth + 1
		bad = append(bad, e)
		e = validEnvelope()
		e.Relay = "eventually"
		bad = append(bad, e)
		e = validEnvelope()
		e.Mailbox = "../other.git"
		bad = append(bad, e)
		for i, envelope := range bad {
			_, err := envelope.Encode()
			Expect(err).To(HaveOccurred(), "case %d", i)
		}
	})
})

var _ = Describe("envelope from hook environment", func() {
	It("reads GIT_PUSH_OPTION_COUNT and the numbered options", func() {
		opts, err := validEnvelope().Encode()
		Expect(err).NotTo(HaveOccurred())
		env := map[string]string{"GIT_PUSH_OPTION_COUNT": strconv.Itoa(len(opts))}
		for i, o := range opts {
			env["GIT_PUSH_OPTION_"+strconv.Itoa(i)] = o
		}
		decoded, err := gitagent.EnvelopeFromEnv(func(k string) string { return env[k] })
		Expect(err).NotTo(HaveOccurred())
		Expect(decoded).To(Equal(validEnvelope()))
	})

	It("names advertisePushOptions when the count is unset", func() {
		_, err := gitagent.EnvelopeFromEnv(func(string) string { return "" })
		Expect(err).To(MatchError(ContainSubstring("advertisePushOptions")))
	})
})

var _ = Describe("envelope↔ref agreement (R4.1)", func() {
	It("accepts matching task and attempt", func() {
		info := gitagent.RefInfo{Task: "my-task", Kind: gitagent.RefResult, Attempt: 2}
		Expect(validEnvelope().MatchesRef(info)).To(Succeed())
	})

	It("rejects disagreement on either field", func() {
		Expect(validEnvelope().MatchesRef(gitagent.RefInfo{Task: "other", Kind: gitagent.RefResult, Attempt: 2})).NotTo(Succeed())
		Expect(validEnvelope().MatchesRef(gitagent.RefInfo{Task: "my-task", Kind: gitagent.RefResult, Attempt: 3})).NotTo(Succeed())
	})
})
