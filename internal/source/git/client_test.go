package git_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/source/git"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testSentinelSecret = "ghp_secretTokenThatMustNeverAppearInLogs"
	testDummyRemoteURL = "https://user:" + testSentinelSecret + "@github.com/org/private-repo.git"
)

func TestRemoteClientErrorRedaction(t *testing.T) {
	t.Parallel()

	client := git.NewRemoteClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cfg := knowl.GitSourceConfig{
		Remote: testDummyRemoteURL,
	}

	_, err := client.ListRemoteRefs(ctx, cfg)
	if err == nil {
		t.Fatal("expected error connecting to dummy remote with fake auth")
	}

	errStr := err.Error()
	if strings.Contains(errStr, testSentinelSecret) {
		t.Fatalf("CRITICAL: sentinel secret leaked in error string: %s", errStr)
	}

	class := git.ClassOfError(err)
	if class == "" {
		t.Errorf("expected classified error, got empty class for %v", err)
	}
}

func TestClassifyTransportErrors(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		rawError  error
		remote    string
		wantClass string
	}{
		{
			name:      "auth required",
			rawError:  errors.New("authentication required for 'https://secret:token@github.com/repo'"),
			remote:    "https://secret:token@github.com/repo",
			wantClass: git.ClassAuthentication,
		},
		{
			name:      "host key verification failed",
			rawError:  errors.New("host key mismatch for ssh://git@github.com"),
			remote:    "git@github.com:repo/name.git",
			wantClass: git.ClassHostVerification,
		},
		{
			name:      "network timeout / reachability",
			rawError:  errors.New("dial tcp 192.0.2.1:443: i/o timeout"),
			remote:    "https://example.com/repo.git",
			wantClass: git.ClassReachability,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrapped := git.WrapClassified(tc.wantClass, tc.rawError, tc.rawError.Error())
			if git.ClassOfError(wrapped) != tc.wantClass {
				t.Errorf("ClassOfError() = %q, want %q", git.ClassOfError(wrapped), tc.wantClass)
			}
			if strings.Contains(wrapped.Error(), "secret:token") {
				t.Errorf("secret leaked in wrapped error: %s", wrapped.Error())
			}
		})
	}
}
