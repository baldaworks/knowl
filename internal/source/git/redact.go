package git

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	userinfoPattern   = regexp.MustCompile(`https?://([^/@:]+:[^/@:]+)@`)
	tokenParamPattern = regexp.MustCompile(`(?i)(token|password|secret|key)=([^&\s]+)`)
	bearerPattern     = regexp.MustCompile(`(?i)bearer\s+([a-zA-Z0-9._~+/-]+=*)`)
	privateKeyPattern = regexp.MustCompile(`-----BEGIN [A-Z ]+ PRIVATE KEY-----[^-]+-----END [A-Z ]+ PRIVATE KEY-----`)
)

// RedactString sanitizes credentials, tokens, sensitive URLs, and private key blocks from text.
func RedactString(text string) string {
	if text == "" {
		return ""
	}
	text = privateKeyPattern.ReplaceAllString(text, "[REDACTED PRIVATE KEY]")
	text = userinfoPattern.ReplaceAllString(text, "https://***@")
	text = tokenParamPattern.ReplaceAllString(text, "$1=***")
	text = bearerPattern.ReplaceAllString(text, "Bearer ***")
	return text
}

// RedactURL returns a credential-free representation of a URL string.
func RedactURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return RedactString(raw)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
