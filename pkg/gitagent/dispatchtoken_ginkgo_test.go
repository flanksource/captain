package gitagent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
)

var _ = Describe("DispatchCredential", func() {
	var path string
	var secret text.SensitiveString

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "keys", gitagent.DispatchCredentialName)
		var err error
		secret, err = gitagent.MintDispatchCredential(path)
		Expect(err).NotTo(HaveOccurred())
	})

	It("hands back a credential the supervisor can present", func() {
		presented, err := captaintoken.Parse(secret.Value())
		Expect(err).NotTo(HaveOccurred())
		Expect(presented.ID).NotTo(BeEmpty())
	})

	It("keeps the plaintext out of the file it wrote", func() {
		raw, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).NotTo(ContainSubstring(secret.Value()))

		// The secret half alone must not survive either — storing it under a
		// different key would defeat the whole point of hashing it.
		_, secretHalf, found := strings.Cut(secret.Value(), ".")
		Expect(found).To(BeTrue())
		Expect(string(raw)).NotTo(ContainSubstring(secretHalf))
	})

	It("stores the verifier readable only by its owner", func() {
		info, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))

		parent, err := os.Stat(filepath.Dir(path))
		Expect(err).NotTo(HaveOccurred())
		Expect(parent.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})

	Describe("Verifier", func() {
		var verifier *captaintoken.Verifier

		BeforeEach(func() {
			credential, err := gitagent.LoadDispatchCredential(path)
			Expect(err).NotTo(HaveOccurred())
			verifier = credential.Verifier("supervisor")
		})

		It("admits the minted secret as the supervisor", func() {
			record, err := verifier.VerifyScope(context.Background(), secret.Value(), captaintoken.ScopeGit)
			Expect(err).NotTo(HaveOccurred())
			Expect(record.Agent).To(Equal("supervisor"))
		})

		It("refuses another agent's credential", func() {
			other, err := gitagent.MintDispatchCredential(filepath.Join(GinkgoT().TempDir(), "other.json"))
			Expect(err).NotTo(HaveOccurred())

			_, err = verifier.Verify(context.Background(), other.Value())
			Expect(err).To(MatchError(captaintoken.ErrUnknown))
		})

		It("refuses a credential that is not a captain token at all", func() {
			_, err := verifier.Verify(context.Background(), "not-a-token")
			Expect(err).To(HaveOccurred())
		})

		// The id is matched first for cost, so a right-id/wrong-secret pair is
		// the case that proves the KDF check is not being skipped.
		It("refuses the right id with the wrong secret", func() {
			id, _, _ := strings.Cut(strings.TrimPrefix(secret.Value(), captaintoken.Prefix+"_"), ".")
			forged := captaintoken.Prefix + "_" + id + ".TvW4gNfLpQ2rXsYbCdEfGhJkLmNoPqRsTuVwXyZ012A"

			_, err := verifier.Verify(context.Background(), forged)
			Expect(err).To(MatchError(captaintoken.ErrUnknown))
		})

		// The credential is git-scoped, so it cannot be replayed against the
		// /api/v1 executor even though the same verifier would recognize it.
		It("refuses the api scope", func() {
			_, err := verifier.VerifyScope(context.Background(), secret.Value(), captaintoken.ScopeAPI)
			Expect(err).To(MatchError(captaintoken.ErrScope))
		})
	})

	// Enrollment re-runs on every restart of the workload, so rotation has to be
	// a replacement. Leaving the previous secret working would be exactly the
	// fallback path CW-3 forbids.
	It("replaces the previous credential rather than adding to it", func() {
		rotated, err := gitagent.MintDispatchCredential(path)
		Expect(err).NotTo(HaveOccurred())

		credential, err := gitagent.LoadDispatchCredential(path)
		Expect(err).NotTo(HaveOccurred())
		verifier := credential.Verifier("supervisor")

		_, err = verifier.Verify(context.Background(), rotated.Value())
		Expect(err).NotTo(HaveOccurred())
		_, err = verifier.Verify(context.Background(), secret.Value())
		Expect(err).To(MatchError(captaintoken.ErrUnknown))
	})

	Describe("LoadDispatchCredential", func() {
		It("names the path it could not read", func() {
			missing := filepath.Join(GinkgoT().TempDir(), "absent.json")
			_, err := gitagent.LoadDispatchCredential(missing)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(missing))
		})

		It("refuses a truncated file rather than verifying nothing", func() {
			truncated := filepath.Join(GinkgoT().TempDir(), "truncated.json")
			Expect(os.WriteFile(truncated, []byte(`{"tokenId":"abc"`), 0o600)).To(Succeed())

			_, err := gitagent.LoadDispatchCredential(truncated)
			Expect(err).To(HaveOccurred())
		})

		// A credential with no hash would make every presented secret verify
		// against an empty string, so it is refused at load rather than at use.
		It("refuses a credential that names no token", func() {
			empty := filepath.Join(GinkgoT().TempDir(), "empty.json")
			Expect(os.WriteFile(empty, []byte(`{}`), 0o600)).To(Succeed())

			_, err := gitagent.LoadDispatchCredential(empty)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nothing could be verified"))
		})
	})
})

// The enrollment field carrying this secret must be a plain string.
// text.SensitiveString marshals to "[REDACTED]" and has no UnmarshalJSON, so
// typing the field for redaction would transmit that literal and the supervisor
// would record a credential the agent rejects at first dispatch.
var _ = Describe("EnrollRequest.DispatchToken", func() {
	const secret = "cptn_aaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	It("round-trips the secret intact", func() {
		encoded, err := json.Marshal(gitagent.EnrollRequest{DispatchToken: secret})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring(secret))

		var decoded gitagent.EnrollRequest
		Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
		Expect(decoded.DispatchToken).To(Equal(secret))
	})

	It("would be redacted on the wire if it were a SensitiveString", func() {
		encoded, err := json.Marshal(struct {
			Token text.SensitiveString `json:"token"`
		}{Token: text.NewSensitiveString(secret)})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).NotTo(ContainSubstring(secret))
	})
})
