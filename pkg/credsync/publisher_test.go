package credsync_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/captain/pkg/agentcreds"
	"github.com/flanksource/captain/pkg/credsync"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Fixed instants, so every scheduling assertion compares against an interval
// worked out here rather than against the publisher's own arithmetic.
var (
	now          = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	claudeExpiry = now.Add(45 * time.Minute)
	codexExpiry  = now.Add(72 * time.Hour)
)

func jwtWithExp(instant time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	claims := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"exp":%d}`, instant.Unix())))
	return header + "." + claims + ".sig"
}

// stubReader serves fixture documents in place of the Keychain and ~/.codex.
func stubReader(claudeExp, codexExp time.Time) agentcreds.Reader {
	claude := fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"claude-access","refreshToken":"claude-refresh","expiresAt":%d}}`,
		claudeExp.UnixMilli())
	codex := fmt.Sprintf(
		`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":%q,"access_token":%q,"refresh_token":"codex-refresh","account_id":"acct"}}`,
		jwtWithExp(codexExp.Add(time.Hour)), jwtWithExp(codexExp))

	return agentcreds.Reader{
		Home: "/fixture/home",
		Now:  func() time.Time { return now },
		ReadFile: func(path string) ([]byte, error) {
			switch {
			case filepath.Base(path) == "auth.json":
				return []byte(codex), nil
			case filepath.Base(path) == ".credentials.json":
				return []byte(claude), nil
			}
			return nil, os.ErrNotExist
		},
	}
}

// recordingTarget captures what it was handed, and can be told to fail.
type recordingTarget struct {
	label     string
	fail      error
	published [][]agentcreds.Credential
}

func (t *recordingTarget) Name() string { return t.label }

func (t *recordingTarget) Publish(_ context.Context, credentials []agentcreds.Credential) error {
	if t.fail != nil {
		return t.fail
	}
	t.published = append(t.published, credentials)
	return nil
}

func newPublisher(target credsync.Target, claudeExp, codexExp time.Time) credsync.Publisher {
	return credsync.Publisher{
		Reader:    stubReader(claudeExp, codexExp),
		Providers: agentcreds.Providers(),
		Targets:   []credsync.Target{target},
		Now:       func() time.Time { return now },
	}
}

// mustPublish publishes and fails the spec on error, returning the result.
// Gomega's Expect(...).Error() cannot be used here: it requires every other
// return value to be zero, and a Result is never zero on success.
func mustPublish(publisher credsync.Publisher) credsync.Result {
	GinkgoHelper()
	result, err := publisher.PublishOnce(context.Background())
	Expect(err).NotTo(HaveOccurred())
	return result
}

var _ = Describe("Publisher.PublishOnce", func() {
	It("hands every target the redacted credential for each provider", func() {
		target := &recordingTarget{label: "test"}
		result, err := newPublisher(target, claudeExpiry, codexExpiry).PublishOnce(context.Background())
		Expect(err).NotTo(HaveOccurred())

		Expect(result.Targets).To(ConsistOf("test"))
		Expect(target.published).To(HaveLen(1))

		delivered := target.published[0]
		Expect(delivered).To(HaveLen(2))
		// The codex document legitimately keeps a `refresh_token` key, so the
		// assertion is about the secret VALUE never surviving, not the field name.
		for _, credential := range delivered {
			payload := string(credential.Payload)
			Expect(payload).NotTo(ContainSubstring("claude-refresh"))
			Expect(payload).NotTo(ContainSubstring("codex-refresh"))
		}
		Expect(result.Published).To(Equal([]credsync.PublishedCredential{
			{
				Provider: "claude", Key: agentcreds.ClaudeFilename,
				Bytes: len(delivered[0].Payload), ExpiresAt: claudeExpiry,
			},
			{
				Provider: "codex", Key: agentcreds.CodexFilename,
				Bytes: len(delivered[1].Payload), ExpiresAt: codexExpiry,
			},
		}))
	})

	It("schedules the next publish a margin before the earliest expiry", func() {
		// Claude expires in 45m and Codex in 72h, so the earliest governs:
		// 45m - 5m margin = 40m, inside the 1m..30m clamp ceiling -> 30m.
		result, err := newPublisher(&recordingTarget{label: "t"}, claudeExpiry, codexExpiry).
			PublishOnce(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(result.NextPublish).To(BeTemporally("==", now.Add(30*time.Minute)))
	})

	It("clamps to the margin when expiry is closer than the margin", func() {
		// 3m to expiry, minus a 5m margin, is negative -> the 1m floor.
		result, err := newPublisher(&recordingTarget{label: "t"}, now.Add(3*time.Minute), codexExpiry).
			PublishOnce(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(result.NextPublish).To(BeTemporally("==", now.Add(time.Minute)))
	})

	It("publishes nothing at all when any source has already expired", func() {
		target := &recordingTarget{label: "t"}
		_, err := newPublisher(target, now.Add(-12*time.Minute), codexExpiry).
			PublishOnce(context.Background())

		Expect(err).To(MatchError(ContainSubstring("claude credential expired 12m0s ago")))
		Expect(err).To(MatchError(ContainSubstring("run `claude`")))
		Expect(target.published).To(BeEmpty(),
			"a live codex credential must not be published when claude is dead — targets stay as they were")
	})

	It("reports a failing target without claiming it was published", func() {
		target := &recordingTarget{label: "broken", fail: fmt.Errorf("permission denied")}
		result, err := newPublisher(target, claudeExpiry, codexExpiry).PublishOnce(context.Background())
		Expect(err).To(MatchError(ContainSubstring("permission denied")))
		Expect(result.Targets).To(BeEmpty())
	})

	It("refuses a configuration with no targets", func() {
		publisher := newPublisher(&recordingTarget{}, claudeExpiry, codexExpiry)
		publisher.Targets = nil
		_, err := publisher.PublishOnce(context.Background())
		Expect(err).To(MatchError(ContainSubstring("no credential targets configured")))
	})
})

var _ = Describe("DirectoryTarget", func() {
	var dir string

	BeforeEach(func() { dir = filepath.Join(GinkgoT().TempDir(), "creds") })

	It("writes private files into a private directory", func() {
		target := credsync.DirectoryTarget{Path: dir}
		_, err := newPublisher(target, claudeExpiry, codexExpiry).PublishOnce(context.Background())
		Expect(err).NotTo(HaveOccurred())

		dirInfo, err := os.Stat(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(dirInfo.Mode().Perm()).To(Equal(os.FileMode(0o700)))

		for _, name := range []string{agentcreds.ClaudeFilename, agentcreds.CodexFilename} {
			info, err := os.Stat(filepath.Join(dir, name))
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)), name)
		}
	})

	It("replaces a previous credential and leaves no temp files behind", func() {
		target := credsync.DirectoryTarget{Path: dir}
		publisher := newPublisher(target, claudeExpiry, codexExpiry)
		mustPublish(publisher)

		first, err := os.ReadFile(filepath.Join(dir, agentcreds.ClaudeFilename))
		Expect(err).NotTo(HaveOccurred())

		// A later expiry is a different document, so the file must change.
		later := newPublisher(target, claudeExpiry.Add(time.Hour), codexExpiry)
		mustPublish(later)

		second, err := os.ReadFile(filepath.Join(dir, agentcreds.ClaudeFilename))
		Expect(err).NotTo(HaveOccurred())
		Expect(second).NotTo(Equal(first))

		entries, err := os.ReadDir(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(2), "only the two credential files remain")
	})

	It("tightens an inherited world-readable directory", func() {
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.Chmod(dir, 0o755)).To(Succeed())

		target := credsync.DirectoryTarget{Path: dir}
		mustPublish(newPublisher(target, claudeExpiry, codexExpiry))

		info, err := os.Stat(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o700)))
	})
})

var _ = Describe("KubernetesTarget", func() {
	It("applies both credentials as Secret keys and converges on republish", func() {
		client := fake.NewClientset()
		target := credsync.KubernetesTarget{Client: client, Namespace: "agents"}

		publisher := newPublisher(target, claudeExpiry, codexExpiry)
		mustPublish(publisher)

		secret, err := client.CoreV1().Secrets("agents").
			Get(context.Background(), credsync.DefaultSecretName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(secret.Data).To(HaveKey(agentcreds.ClaudeFilename))
		Expect(secret.Data).To(HaveKey(agentcreds.CodexFilename))
		Expect(string(secret.Data[agentcreds.ClaudeFilename])).NotTo(ContainSubstring("claude-refresh"))

		// A second publish must update in place rather than fail on AlreadyExists.
		later := newPublisher(target, claudeExpiry.Add(time.Hour), codexExpiry)
		mustPublish(later)

		updated, err := client.CoreV1().Secrets("agents").
			Get(context.Background(), credsync.DefaultSecretName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Data[agentcreds.ClaudeFilename]).
			NotTo(Equal(secret.Data[agentcreds.ClaudeFilename]))
	})

	It("refuses to publish without a namespace rather than guessing one", func() {
		target := credsync.KubernetesTarget{Client: fake.NewClientset()}
		_, err := newPublisher(target, claudeExpiry, codexExpiry).PublishOnce(context.Background())
		Expect(err).To(MatchError(ContainSubstring("no namespace")))
	})
})
