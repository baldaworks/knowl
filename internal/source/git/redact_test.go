package git_test

import (
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/source/git"
)

func TestRedactString(t *testing.T) {
	t.Parallel()
	const (
		sentinelUser = "my-user"
		sentinelPass = "super-secret-token"
		rawURL       = "https://" + sentinelUser + ":" + sentinelPass + "@github.com/org/repo.git"
		tokenQuery   = "https://github.com/org/repo?token=" + sentinelPass
		bearerHeader = "Authorization: Bearer " + sentinelPass
		privateKey   = "-----BEGIN OPENSSH PRIVATE KEY-----\nsecretkeydata\n-----END OPENSSH PRIVATE KEY-----"
	)

	for _, tc := range []struct {
		name      string
		input     string
		forbidden []string
	}{
		{name: "userinfo in url", input: rawURL, forbidden: []string{sentinelPass, sentinelUser + ":"}},
		{name: "token query", input: tokenQuery, forbidden: []string{sentinelPass}},
		{name: "bearer auth", input: bearerHeader, forbidden: []string{sentinelPass}},
		{name: "private key block", input: privateKey, forbidden: []string{"secretkeydata"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := git.RedactString(tc.input)
			for _, f := range tc.forbidden {
				if strings.Contains(got, f) {
					t.Errorf("RedactString(%q) = %q, still contains forbidden text %q", tc.input, got, f)
				}
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	t.Parallel()
	raw := "https://user:token123@github.com/org/repo.git?param=value#frag"
	redacted := git.RedactURL(raw)
	if strings.Contains(redacted, "token123") || strings.Contains(redacted, "user") {
		t.Errorf("RedactURL(%q) = %q, expected userinfo stripped", raw, redacted)
	}
	if strings.Contains(redacted, "param=value") || strings.Contains(redacted, "frag") {
		t.Errorf("RedactURL(%q) = %q, expected query and fragment stripped", raw, redacted)
	}
}
