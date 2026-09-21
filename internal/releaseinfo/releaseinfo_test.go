package releaseinfo

import (
	"errors"
	"testing"
)

const (
	testReleaseVersion = "0.6.0"
	testReleaseTag     = "v0.6.0"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    Identity
		wantErr error
	}{
		{name: "development", value: "dev", want: Identity{Version: "dev"}},
		{name: "version", value: testReleaseVersion, want: Identity{Version: testReleaseVersion, Tag: testReleaseTag, Release: true}},
		{name: "tag", value: testReleaseTag, want: Identity{Version: testReleaseVersion, Tag: testReleaseTag, Release: true}},
		{name: "missing", wantErr: ErrInvalidVersion},
		{name: "leading zero", value: "v0.06.0", wantErr: ErrInvalidVersion},
		{name: "partial", value: "0.6", wantErr: ErrInvalidVersion},
		{name: "mutable", value: "latest", wantErr: ErrInvalidVersion},
		{name: "whitespace", value: " 0.6.0", wantErr: ErrInvalidVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(test.value)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Parse(%q) error = %v, want %v", test.value, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("Parse(%q) = %#v, want %#v", test.value, got, test.want)
			}
		})
	}
}

func TestCurrentReleaseRejectsDevelopmentBuild(t *testing.T) {
	original := Version
	Version = DevelopmentVersion
	t.Cleanup(func() { Version = original })

	if _, err := CurrentRelease(); !errors.Is(err, ErrDevelopmentBuild) {
		t.Fatalf("CurrentRelease() error = %v, want %v", err, ErrDevelopmentBuild)
	}
}
