package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

// The two credential flags belong to different targets. Ignoring the wrong one
// would leave an operator with a deploy that looks configured and a sidecar with
// no login, so each is refused rather than dropped.

func TestCredentialsSecretRequiresKubernetes(t *testing.T) {
	var plan deploy.Plan
	err := applyCredentialMount(&plan,
		GitAgentDeployOptions{CredentialsSecret: "captain-agent-credentials"}, deploy.TargetDocker)

	if err == nil {
		t.Fatal("--credentials-secret was accepted on a docker deploy")
	}
	if !strings.Contains(err.Error(), "--credentials-dir for docker") {
		t.Errorf("error does not name the right flag: %v", err)
	}
}

func TestCredentialsDirRequiresDocker(t *testing.T) {
	var plan deploy.Plan
	err := applyCredentialMount(&plan,
		GitAgentDeployOptions{CredentialsDir: t.TempDir()}, deploy.TargetKubernetes)

	if err == nil {
		t.Fatal("--credentials-dir was accepted on a kubernetes deploy")
	}
	if !strings.Contains(err.Error(), "--credentials-secret for kubernetes") {
		t.Errorf("error does not name the right flag: %v", err)
	}
}

func TestCredentialFlagsAreMutuallyExclusive(t *testing.T) {
	var plan deploy.Plan
	err := applyCredentialMount(&plan, GitAgentDeployOptions{
		CredentialsSecret: "s", CredentialsDir: t.TempDir(),
	}, deploy.TargetKubernetes)

	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v", err)
	}
}

func TestCredentialsDirIsResolvedAndMustExist(t *testing.T) {
	// A path docker cannot bind-mount produces a container that fails to start,
	// long after deploy has already minted a durable token.
	var missing deploy.Plan
	err := applyCredentialMount(&missing,
		GitAgentDeployOptions{CredentialsDir: filepath.Join(t.TempDir(), "never-synced")},
		deploy.TargetDocker)
	if err == nil {
		t.Fatal("a missing credentials directory was accepted")
	}
	if !strings.Contains(err.Error(), "credentials sync") {
		t.Errorf("error does not name the command that creates it: %v", err)
	}

	dir := t.TempDir()
	var present deploy.Plan
	if err := applyCredentialMount(&present,
		GitAgentDeployOptions{CredentialsDir: dir}, deploy.TargetDocker); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(present.CredentialsDir) {
		t.Errorf("CredentialsDir = %q, want an absolute path", present.CredentialsDir)
	}
}

func TestCredentialsDirRejectsParentTraversal(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "credentials")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	requested := filepath.Join(root, "unused") + string(filepath.Separator) + ".." +
		string(filepath.Separator) + "credentials"

	var plan deploy.Plan
	err := applyCredentialMount(&plan,
		GitAgentDeployOptions{CredentialsDir: requested}, deploy.TargetDocker)

	if err == nil || !strings.Contains(err.Error(), "must not contain '..'") {
		t.Fatalf("err = %v, want a parent-traversal refusal", err)
	}
	if plan.CredentialsDir != "" {
		t.Fatalf("CredentialsDir = %q after refusal", plan.CredentialsDir)
	}
}

func TestNoCredentialFlagsLeavesThePlanUnmounted(t *testing.T) {
	var plan deploy.Plan
	if err := applyCredentialMount(&plan, GitAgentDeployOptions{}, deploy.TargetKubernetes); err != nil {
		t.Fatal(err)
	}
	if plan.CredentialsSecret != "" || plan.CredentialsDir != "" {
		t.Errorf("plan gained a credential mount from nothing: %+v", plan)
	}
}
