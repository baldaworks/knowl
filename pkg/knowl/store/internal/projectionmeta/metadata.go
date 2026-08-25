// Package projectionmeta defines the shared OKF projection encoding used by
// both durable search adapters.
package projectionmeta

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

// OKFFormat is the discriminator persisted with OKF v0.2 projection metadata.
const OKFFormat = "okf/0.2"

// PageValues returns normalized, transport-safe projection values. OKF pages
// use their separately parsed user body, including a legitimately empty body.
func PageValues(page knowl.PageSnapshot) (format, description, body string, metadata []byte, err error) {
	body = page.Body
	if page.OKF == nil {
		if body == "" {
			body = knowlwiki.Body(page.Content)
		}
		return "", "", body, nil, nil
	}

	metadata, err = json.Marshal(page.OKF)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("encode OKF metadata: %w", err)
	}
	return OKFFormat, page.OKF.Description, body, metadata, nil
}

// Decode validates a stored format discriminator and returns a detached OKF
// metadata value. Legacy rows have both values absent.
func Decode(format string, metadata []byte) (*okf.Metadata, error) {
	if format == "" {
		if len(bytes.TrimSpace(metadata)) == 0 || bytes.Equal(bytes.TrimSpace(metadata), []byte("null")) {
			return nil, nil
		}
		return nil, fmt.Errorf("OKF metadata has no format discriminator")
	}
	if format != OKFFormat {
		return nil, fmt.Errorf("unsupported projection format %q", format)
	}
	if len(bytes.TrimSpace(metadata)) == 0 || bytes.Equal(bytes.TrimSpace(metadata), []byte("null")) {
		return nil, fmt.Errorf("projection format %q has no metadata", format)
	}
	var value okf.Metadata
	if err := json.Unmarshal(metadata, &value); err != nil {
		return nil, fmt.Errorf("decode OKF metadata: %w", err)
	}
	return &value, nil
}
