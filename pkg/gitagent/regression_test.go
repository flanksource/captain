package gitagent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestSplitSSHEndpointBracketedIPv6DefaultsPort(t *testing.T) {
	addr, user, err := splitSSHEndpoint("ssh://operator@[::1]")
	if err != nil {
		t.Fatal(err)
	}
	if addr != "[::1]:22" || user != "operator" {
		t.Fatalf("endpoint = %q, %q", addr, user)
	}
}

func TestUpdateTaskStateSerializesConcurrentWriters(t *testing.T) {
	repo := t.TempDir()
	if err := SaveTaskState(repo, &TaskState{Task: "t-lock"}); err != nil {
		t.Fatal(err)
	}
	const writers = 24
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := UpdateTaskState(repo, "t-lock", func(st *TaskState) (bool, error) {
				st.Attempts++
				return true, nil
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, ok, err := LoadTaskState(repo, "t-lock")
	if err != nil || !ok {
		t.Fatalf("load state: ok=%v err=%v", ok, err)
	}
	if state.Attempts != writers {
		t.Fatalf("attempts = %d, want %d", state.Attempts, writers)
	}
}

func TestEnsureKeyPairConcurrentCreationReturnsStoredKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent_ed25519")
	const callers = 8
	type result struct {
		fingerprint string
		err         error
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fingerprint, err := EnsureKeyPair(path)
			results <- result{fingerprint: fingerprint, err: err}
		}()
	}
	wg.Wait()
	close(results)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	want := gossh.FingerprintSHA256(signer.PublicKey())
	for result := range results {
		if result.err != nil || result.fingerprint != want {
			t.Fatalf("result = %#v, stored fingerprint = %q", result, want)
		}
	}
}

func TestRefUpdateRecognizesSHA256NullOID(t *testing.T) {
	null := "0000000000000000000000000000000000000000000000000000000000000000"
	if !(RefUpdate{Old: null}).IsCreate() || !(RefUpdate{New: null}).IsDelete() {
		t.Fatal("64-character null OID was not recognized")
	}
}

func TestSSHTransportCommandQuotesExecutable(t *testing.T) {
	got := SSHTransportCommand("/tmp/captain build/captain's")
	want := "'/tmp/captain build/captain'\"'\"'s' sandbox git-agent ssh"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestWriteFileAtomicCleansTempAfterRenameFailure(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "state.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("state"), 0o644); err == nil {
		t.Fatal("writeFileAtomic succeeded with a directory target")
	}
	matches, err := filepath.Glob(filepath.Join(parent, ".state.json-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestRecordDispatchCreatesAuditRefsAtomically(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	env := ScrubGitEnv(os.Environ())
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"-c", "user.name=test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "base"},
	} {
		if _, err := runGit(ctx, repo, env, args...); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := TakeSnapshot(ctx, repo, SnapshotPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	control, err := BuildControlCommit(ctx, repo, nil, map[string][]byte{ControlTaskFile: []byte(`{"prompt":"test"}`)})
	if err != nil {
		t.Fatal(err)
	}
	mailbox := filepath.Join(t.TempDir(), "mailbox.git")
	if err := InitMailbox(ctx, mailbox, repo); err != nil {
		t.Fatal(err)
	}
	controlRef, _ := ControlRef("t-atomic", 1)
	if _, err := runGit(ctx, mailbox, env, "update-ref", controlRef, control); err != nil {
		t.Fatal(err)
	}
	req := DispatchRequest{RepoDir: repo, MailboxPath: mailbox, Task: "t-atomic"}
	if err := recordDispatch(ctx, req, "t-atomic", snapshot, control); err == nil {
		t.Fatal("recordDispatch succeeded despite an existing control ref")
	}
	dispatchRef, _ := DispatchRef("t-atomic", 1)
	code, _, err := gitExitCode(ctx, mailbox, env, "show-ref", "--verify", "--quiet", dispatchRef)
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Fatalf("dispatch ref %s was left behind after transaction failure", dispatchRef)
	}
}
