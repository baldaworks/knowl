// Package releaseinfo exposes the build's normalized release identity.
package releaseinfo

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const DevelopmentVersion = "dev"

var (
	// Version is replaced for release builds with:
	// -ldflags "-X github.com/baldaworks/knowl/internal/releaseinfo.Version=vX.Y.Z".
	Version = DevelopmentVersion

	ErrDevelopmentBuild = errors.New("development build has no release identity")
	ErrInvalidVersion   = errors.New("invalid release version")
)

// Identity is the normalized version information emitted by the CLI.
type Identity struct {
	Version string `json:"version"`
	Tag     string `json:"tag,omitempty"`
	Release bool   `json:"release"`
}

// Current parses the release identity embedded in this build.
func Current() (Identity, error) {
	return Parse(Version)
}

// CurrentRelease returns the embedded identity only for a valid release build.
func CurrentRelease() (Identity, error) {
	identity, err := Current()
	if err != nil {
		return Identity{}, err
	}
	if !identity.Release {
		return Identity{}, ErrDevelopmentBuild
	}
	return identity, nil
}

// Parse normalizes a development marker or a stable semantic version/tag.
func Parse(value string) (Identity, error) {
	if value == DevelopmentVersion {
		return Identity{Version: DevelopmentVersion}, nil
	}
	if value == "" || strings.TrimSpace(value) != value {
		return Identity{}, fmt.Errorf("%w: %q", ErrInvalidVersion, value)
	}

	normalized := strings.TrimPrefix(value, "v")
	valid, err := regexp.MatchString(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`, normalized)
	if err != nil {
		return Identity{}, fmt.Errorf("compile release version expression: %w", err)
	}
	if !valid {
		return Identity{}, fmt.Errorf("%w: %q", ErrInvalidVersion, value)
	}
	return Identity{Version: normalized, Tag: "v" + normalized, Release: true}, nil
}
