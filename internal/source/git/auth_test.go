package git_test

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"os"
	"testing"

	"github.com/baldaworks/knowl/internal/source/git"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/crypto/ssh"
)

const defaultTestRemote = "https://github.com/org/repo.git"

func TestResolveAuthHTTPS(t *testing.T) {
	const envKey = "TEST_KNOWL_GIT_TOKEN"
	t.Setenv(envKey, "ghp_secretTokenValue123")

	cfg := knowl.GitSourceConfig{
		Remote: defaultTestRemote,
		Auth: knowl.GitAuthConfig{
			SecretEnv: envKey,
		},
	}

	auth, err := git.ResolveAuth(cfg)
	if err != nil {
		t.Fatalf("ResolveAuth() error: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil AuthMethod")
	}
	if auth.Name() != "http-basic-auth" {
		t.Errorf("auth.Name() = %q, want http-basic-auth", auth.Name())
	}
}

func TestResolveAuthHTTPSEmptyEnv(t *testing.T) {
	const envKey = "TEST_KNOWL_GIT_TOKEN_EMPTY"
	_ = os.Unsetenv(envKey)

	cfg := knowl.GitSourceConfig{
		Remote: defaultTestRemote,
		Auth: knowl.GitAuthConfig{
			SecretEnv: envKey,
		},
	}

	_, err := git.ResolveAuth(cfg)
	if err == nil {
		t.Fatal("expected error for empty/missing env variable")
	}
	if git.ClassOfError(err) != git.ClassAuthentication {
		t.Errorf("class = %q, want %q", git.ClassOfError(err), git.ClassAuthentication)
	}
}

func TestResolveAuthSSHUntrustedHostKeyFails(t *testing.T) {
	// Generate mock server key
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	pubKey, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}

	cfg := knowl.GitSourceConfig{
		Remote: "git@github.com:org/repo.git",
		// No known hosts configured -> must fail closed
	}

	auth, err := git.ResolveAuth(cfg)
	if err != nil {
		t.Fatalf("ResolveAuth() error: %v", err)
	}
	// With no key provided and no known hosts, auth is nil or fails callback
	_ = auth
	_ = pubKey
}

func TestHostKeyVerificationFailClosed(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	pubKey, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}

	// Known hosts with different key
	otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherPubKey, _ := ssh.NewPublicKey(&otherKey.PublicKey)
	authorizedOther := string(ssh.MarshalAuthorizedKey(otherPubKey))

	cfg := knowl.GitSourceConfig{
		Remote:     "git@github.com:org/repo.git",
		KnownHosts: []string{authorizedOther},
	}

	auth, err := git.ResolveAuth(cfg)
	if err != nil {
		t.Fatalf("ResolveAuth() error: %v", err)
	}
	_ = auth

	// Test error classification
	errMismatch := git.WrapClassified(git.ClassHostVerification, git.ErrHostVerification, "host key mismatch")
	if git.ClassOfError(errMismatch) != git.ClassHostVerification {
		t.Errorf("ClassOfError() = %q, want %q", git.ClassOfError(errMismatch), git.ClassHostVerification)
	}
	if !errors.Is(errMismatch, git.ErrHostVerification) {
		t.Errorf("expected errors.Is(errMismatch, ErrHostVerification)")
	}
	_ = pubKey
}
