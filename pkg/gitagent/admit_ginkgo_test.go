package gitagent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/gitagent"
)

const zeroOID40 = "0000000000000000000000000000000000000000"

// admitFixture builds a supervisor repo with a snapshot, a control commit,
// and an initialized sidecar holding both (pushed without admission, so the
// tests can drive Admit directly against real objects).
type admitFixture struct {
	super, sidecar string
	snap           *gitagent.Snapshot
	control        string
	env            []string
}

func newAdmitFixture(ctx context.Context) *admitFixture {
	super := GinkgoT().TempDir()
	gitT(super, "init", "-q")
	writeFileT(super, "src/main.go", "package main\n")
	gitT(super, "add", "-A")
	gitT(super, "commit", "-q", "-m", "base")
	writeFileT(super, "src/dirty.go", "package main // dirty\n")

	snap, err := gitagent.TakeSnapshot(ctx, super, gitagent.SnapshotPolicy{})
	Expect(err).NotTo(HaveOccurred())
	control, err := gitagent.BuildControlCommit(ctx, super, nil, map[string][]byte{
		gitagent.ControlTaskFile:   []byte(`{"prompt":"do the thing"}`),
		gitagent.ControlHooksFile:  []byte(`{}`),
		gitagent.ControlPolicyFile: []byte(`{}`),
	})
	Expect(err).NotTo(HaveOccurred())

	sidecar := filepath.Join(GinkgoT().TempDir(), "repo.git")
	Expect(gitagent.InitSidecar(ctx, sidecar)).To(Succeed())
	gitT(super, "push", "-q", sidecar,
		snap.Commit+":refs/captain/tasks/t-1/dispatch/1",
		control+":refs/captain/tasks/t-1/control/1")
	return &admitFixture{super: super, sidecar: sidecar, snap: snap, control: control, env: os.Environ()}
}

func (f *admitFixture) envelope() *gitagent.Envelope {
	return &gitagent.Envelope{
		Version: gitagent.ProtocolVersion,
		Task:    "t-1",
		Attempt: 1,
		Base:    f.snap.Base,
		Depth:   0,
		Agent:   "worker-1",
		Relay:   gitagent.RelaySync,
		Mailbox: "mailboxes/" + strings.Repeat("a", 64) + ".git",
	}
}

func (f *admitFixture) dispatchUpdates() []gitagent.RefUpdate {
	return []gitagent.RefUpdate{
		{Old: zeroOID40, New: f.snap.Commit, Ref: "refs/captain/tasks/t-1/dispatch/1"},
		{Old: zeroOID40, New: f.control, Ref: "refs/captain/tasks/t-1/control/1"},
	}
}

// newCommitOn writes files on top of parent in repo and returns the commit,
// leaving the objects in the repo with no ref pointing at them (as quarantine
// would).
func newCommitOn(ctx context.Context, repo, parent string, files map[string]string) string {
	GinkgoHelper()
	work := GinkgoT().TempDir()
	gitT(repo, "worktree", "add", "-q", "--detach", work, parent)
	defer gitT(repo, "worktree", "remove", "--force", work)
	for path, content := range files {
		writeFileT(work, path, content)
	}
	gitT(work, "add", "-f", "-A")
	gitT(work, "commit", "-q", "-m", "agent work")
	return gitT(work, "rev-parse", "HEAD")
}

var _ = Describe("admission", func() {
	ctx := context.Background()

	It("parses pre-receive stdin", func() {
		updates, err := gitagent.ParseRefUpdates(strings.NewReader(
			zeroOID40 + " " + strings.Repeat("a", 40) + " refs/captain/tasks/t-1/dispatch/1\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(updates).To(HaveLen(1))
		Expect(updates[0].IsCreate()).To(BeTrue())
		Expect(updates[0].IsDelete()).To(BeFalse())

		_, err = gitagent.ParseRefUpdates(strings.NewReader("garbage\n"))
		Expect(err).To(HaveOccurred())
	})

	It("admits a well-formed dispatch pair on the sidecar", func() {
		f := newAdmitFixture(ctx)
		Expect(gitagent.Admit(ctx, gitagent.AdmitRequest{
			Repo: f.sidecar, Role: gitagent.RoleSidecar,
			Updates: f.dispatchUpdates(), Envelope: f.envelope(), Env: f.env,
		})).To(Succeed())
	})

	It("rejects deletes, updates to existing refs, and unpaired refs (R3.2/R3.4)", func() {
		f := newAdmitFixture(ctx)

		del := f.dispatchUpdates()
		del[0].New = zeroOID40
		err := gitagent.Admit(ctx, gitagent.AdmitRequest{Repo: f.sidecar, Role: gitagent.RoleSidecar, Updates: del, Envelope: f.envelope(), Env: f.env})
		Expect(err).To(MatchError(ContainSubstring("cannot be deleted")))

		upd := f.dispatchUpdates()
		upd[0].Old = f.snap.Base
		err = gitagent.Admit(ctx, gitagent.AdmitRequest{Repo: f.sidecar, Role: gitagent.RoleSidecar, Updates: upd, Envelope: f.envelope(), Env: f.env})
		Expect(err).To(MatchError(ContainSubstring("every protocol ref push is a create")))

		// A code ref alone is unprocessable — unless the receiver already
		// holds that attempt's control ref, which the fixture's task does.
		Expect(gitagent.Admit(ctx, gitagent.AdmitRequest{Repo: f.sidecar, Role: gitagent.RoleSidecar, Updates: f.dispatchUpdates()[:1], Envelope: f.envelope(), Env: f.env})).
			To(Succeed(), "an existing control ref satisfies the pairing")

		fresh := f.envelope()
		fresh.Task = "t-2"
		err = gitagent.Admit(ctx, gitagent.AdmitRequest{
			Repo: f.sidecar, Role: gitagent.RoleSidecar,
			Updates:  []gitagent.RefUpdate{{Old: zeroOID40, New: f.snap.Commit, Ref: "refs/captain/tasks/t-2/dispatch/1"}},
			Envelope: fresh, Env: f.env,
		})
		Expect(err).To(MatchError(ContainSubstring("R3.4")))
	})

	It("rejects role mismatches, missing envelopes and envelope disagreement (R4.1)", func() {
		f := newAdmitFixture(ctx)

		err := gitagent.Admit(ctx, gitagent.AdmitRequest{Repo: f.sidecar, Role: gitagent.RoleMailbox, Updates: f.dispatchUpdates(), Envelope: f.envelope(), Env: f.env})
		Expect(err).To(MatchError(ContainSubstring("does not accept dispatch")))

		err = gitagent.Admit(ctx, gitagent.AdmitRequest{Repo: f.sidecar, Role: gitagent.RoleSidecar, Updates: f.dispatchUpdates(), Env: f.env})
		Expect(err).To(MatchError(ContainSubstring("require the control envelope")))

		unroutable := f.envelope()
		unroutable.Mailbox = ""
		err = gitagent.Admit(ctx, gitagent.AdmitRequest{Repo: f.sidecar, Role: gitagent.RoleSidecar, Updates: f.dispatchUpdates(), Envelope: unroutable, Env: f.env})
		Expect(err).To(MatchError(ContainSubstring("no mailbox route")))

		wrong := f.envelope()
		wrong.Attempt = 2
		err = gitagent.Admit(ctx, gitagent.AdmitRequest{Repo: f.sidecar, Role: gitagent.RoleSidecar, Updates: f.dispatchUpdates(), Envelope: wrong, Env: f.env})
		Expect(err).To(MatchError(ContainSubstring("disagrees")))

		err = gitagent.Admit(ctx, gitagent.AdmitRequest{
			Repo: f.sidecar, Role: gitagent.RoleSidecar,
			Updates:  []gitagent.RefUpdate{{Old: zeroOID40, New: f.snap.Commit, Ref: "refs/heads/main"}},
			Envelope: f.envelope(), Env: f.env,
		})
		Expect(err).To(MatchError(ContainSubstring("outside the protocol namespaces")))
	})

	Describe("agent branch pushes on the sidecar", func() {
		It("requires a dispatched task and fast-forward updates", func() {
			f := newAdmitFixture(ctx)
			work := newCommitOn(ctx, f.sidecar, f.snap.Commit, map[string]string{"src/fix.go": "package main // fix\n"})

			err := gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: f.sidecar, Role: gitagent.RoleSidecar,
				Updates: []gitagent.RefUpdate{{Old: zeroOID40, New: work, Ref: "refs/heads/captain/t-1"}},
				Env:     f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("never dispatched")))

			Expect(gitagent.SaveTaskState(f.sidecar, &gitagent.TaskState{
				Task: "t-1", Agent: "worker-1", Base: f.snap.Base, DispatchCommit: f.snap.Commit,
			})).To(Succeed())

			Expect(gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: f.sidecar, Role: gitagent.RoleSidecar,
				Updates: []gitagent.RefUpdate{{Old: zeroOID40, New: work, Ref: "refs/heads/captain/t-1"}},
				Env:     f.env,
			})).To(Succeed())

			// A push rewinding to the dispatch tip is not a fast-forward.
			err = gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: f.sidecar, Role: gitagent.RoleSidecar,
				Updates: []gitagent.RefUpdate{{Old: work, New: f.snap.Commit, Ref: "refs/heads/captain/t-1"}},
				Env:     f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("non-fast-forward")))
		})

		It("applies the content gates to agent work", func() {
			f := newAdmitFixture(ctx)
			Expect(gitagent.SaveTaskState(f.sidecar, &gitagent.TaskState{
				Task: "t-1", Agent: "worker-1", Base: f.snap.Base, DispatchCommit: f.snap.Commit,
				Policy: gitagent.Policy{Paths: []string{"src/**"}, MaxBlobSize: 64},
			})).To(Succeed())

			secret := newCommitOn(ctx, f.sidecar, f.snap.Commit, map[string]string{"src/.env": "TOKEN=x\n"})
			err := gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: f.sidecar, Role: gitagent.RoleSidecar,
				Updates: []gitagent.RefUpdate{{Old: zeroOID40, New: secret, Ref: "refs/heads/captain/t-1"}},
				Env:     f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("gate:secret-name")))

			outside := newCommitOn(ctx, f.sidecar, f.snap.Commit, map[string]string{"docs/notes.md": "notes\n"})
			err = gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: f.sidecar, Role: gitagent.RoleSidecar,
				Updates: []gitagent.RefUpdate{{Old: zeroOID40, New: outside, Ref: "refs/heads/captain/t-1"}},
				Env:     f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("gate:path-denied")))

			big := newCommitOn(ctx, f.sidecar, f.snap.Commit, map[string]string{"src/big.go": strings.Repeat("x", 128) + "\n"})
			err = gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: f.sidecar, Role: gitagent.RoleSidecar,
				Updates: []gitagent.RefUpdate{{Old: zeroOID40, New: big, Ref: "refs/heads/captain/t-1"}},
				Env:     f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("gate:blob-size")))
		})
	})

	Describe("result admission on the mailbox", func() {
		It("enforces parentage, agent namespace and attempt caps", func() {
			f := newAdmitFixture(ctx)
			mailbox := filepath.Join(GinkgoT().TempDir(), "mailbox.git")
			Expect(gitagent.InitMailbox(ctx, mailbox, f.super)).To(Succeed())
			Expect(gitagent.SaveTaskState(mailbox, &gitagent.TaskState{
				Task: "t-1", Agent: "worker-1", Base: f.snap.Base, DispatchCommit: f.snap.Commit,
				Policy: gitagent.Policy{MaxAttempts: 2},
			})).To(Succeed())

			result := newCommitOn(ctx, f.super, f.snap.Commit, map[string]string{"src/fix.go": "package main // fix\n"})
			resultUpdates := []gitagent.RefUpdate{
				{Old: zeroOID40, New: result, Ref: "refs/captain/tasks/t-1/result/1"},
				{Old: zeroOID40, New: f.control, Ref: "refs/captain/tasks/t-1/control/1"},
			}

			// Admission must stay inside this task's dispatch..result range. A
			// damaged historical namespace cannot reject an unrelated result.
			broken := filepath.Join(mailbox, "refs", "captain", "tasks", "t-stale", "control", "1")
			Expect(os.MkdirAll(filepath.Dir(broken), 0o755)).To(Succeed())
			Expect(os.WriteFile(broken, []byte(strings.Repeat("f", 40)+"\n"), 0o644)).To(Succeed())

			Expect(gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: mailbox, Role: gitagent.RoleMailbox, Agent: "worker-1",
				Updates: resultUpdates, Envelope: f.envelope(), Env: f.env,
			})).To(Succeed())

			forgedBase := f.envelope()
			forgedBase.Base = result
			err := gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: mailbox, Role: gitagent.RoleMailbox, Agent: "worker-1",
				Updates: resultUpdates, Envelope: forgedBase, Env: f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("does not match dispatched base")))

			err = gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: mailbox, Role: gitagent.RoleMailbox, Agent: "worker-2",
				Updates: resultUpdates, Envelope: f.envelope(), Env: f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("belongs to agent")))

			orphan := newCommitOn(ctx, f.super, f.snap.Base, map[string]string{"src/fix.go": "package main // fix\n"})
			orphanUpdates := []gitagent.RefUpdate{
				{Old: zeroOID40, New: orphan, Ref: "refs/captain/tasks/t-1/result/1"},
				{Old: zeroOID40, New: f.control, Ref: "refs/captain/tasks/t-1/control/1"},
			}
			err = gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: mailbox, Role: gitagent.RoleMailbox, Agent: "worker-1",
				Updates: orphanUpdates, Envelope: f.envelope(), Env: f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("parented on its dispatch")))

			over := f.envelope()
			over.Attempt = 3
			overUpdates := []gitagent.RefUpdate{
				{Old: zeroOID40, New: result, Ref: "refs/captain/tasks/t-1/result/3"},
				{Old: zeroOID40, New: f.control, Ref: "refs/captain/tasks/t-1/control/3"},
			}
			err = gitagent.Admit(ctx, gitagent.AdmitRequest{
				Repo: mailbox, Role: gitagent.RoleMailbox, Agent: "worker-1",
				Updates: overUpdates, Envelope: over, Env: f.env,
			})
			Expect(err).To(MatchError(ContainSubstring("maxAttempts")))
		})
	})
})
