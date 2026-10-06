package webui

import "testing"

func TestSafeURI(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://u:p@example.com/path?q=secret#fragment", "https://example.com/path"},
		{"file:///private/path", ""}, {"javascript:alert(1)", ""}, {"data:text/html,test", ""}, {"//example.com/path", ""}, {"/private/path", ""}, {"http://example.com/", "http://example.com/"},
	} {
		if got := SafeURI(tc.input); got != tc.want {
			t.Errorf("SafeURI=%q want %q", got, tc.want)
		}
	}
}
