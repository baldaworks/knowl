package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func ingestEnvelope(scope knowl.ScopeRef, arguments map[string]any) (knowl.SourceEnvelope, error) {
	content, _ := optionalString(arguments, "content")
	uri, _ := optionalString(arguments, "uri")
	mediaType, _ := optionalString(arguments, "media_type")
	origin, _ := optionalString(arguments, "origin")
	idempotencyKey, _ := optionalString(arguments, "idempotency_key")

	switch {
	case content == "" && uri == "":
		return knowl.SourceEnvelope{}, fmt.Errorf("one of %q or %q is required: %w", "content", "uri", ErrInvalidArguments)
	case content != "" && uri != "":
		return knowl.SourceEnvelope{}, fmt.Errorf("only one of %q or %q is allowed: %w", "content", "uri", ErrInvalidArguments)
	}

	payload := []byte(content)
	adapter := inlineSourceAdapter
	sourceHint := origin
	if uri != "" {
		payload = []byte(uri)
		adapter = "uri"
		sourceHint = uri
		if mediaType == "" {
			mediaType = "text/uri-list"
		}
	}
	if mediaType == "" {
		mediaType = "text/plain"
	}
	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	sourceID := stableSourceID(sourceHint, idempotencyKey, digestText)
	version := stableSourceVersion(idempotencyKey, digestText)
	return knowl.SourceEnvelope{
		Scope:     scope,
		Source:    knowl.SourceRef{Adapter: adapter, ID: sourceID},
		Version:   knowl.SourceVersion{Version: version, Digest: digestText},
		MediaType: mediaType,
		Content:   payload,
	}, nil
}

func stableSourceID(origin, idempotencyKey, digest string) string {
	switch {
	case origin != "":
		return strings.TrimSpace(origin)
	case idempotencyKey != "":
		return strings.TrimSpace(idempotencyKey)
	default:
		return "source-" + digest[:16]
	}
}

func stableSourceVersion(idempotencyKey, digest string) string {
	if idempotencyKey != "" {
		return strings.TrimSpace(idempotencyKey)
	}
	return "sha256-" + digest[:16]
}
