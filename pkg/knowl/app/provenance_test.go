package app_test

import (
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func TestCanonicalProvenanceLimitsDefaultAndRejectUnsafeValues(t *testing.T) {
	defaults, err := app.NormalizeCanonicalProvenanceLimits(app.CanonicalProvenanceLimits{})
	if err != nil {
		t.Fatalf("default limits: %v", err)
	}
	if defaults.Returned != 32 || defaults.Examined != 1_024 || defaults.PathBytes != 2_048 || defaults.PageBytes != 4<<20 ||
		defaults.TotalBytes != 4<<20 || defaults.SourceRefs != 64 || defaults.RoundPages != 100_000 {
		t.Fatalf("default limits = %#v", defaults)
	}
	maximum := app.CanonicalProvenanceLimits{
		Returned: 32, Examined: 1_024, PathBytes: 2_048,
		PageBytes: 4 << 20, TotalBytes: 4 << 20, SourceRefs: 64, RoundPages: 100_000,
	}
	if normalized, err := app.NormalizeCanonicalProvenanceLimits(maximum); err != nil || normalized != maximum {
		t.Fatalf("maximum limits = %#v, %v", normalized, err)
	}

	for _, limits := range []app.CanonicalProvenanceLimits{
		{Returned: 33, Examined: 1, PathBytes: 1, PageBytes: 1, TotalBytes: 1, SourceRefs: 1, RoundPages: 1},
		{Returned: 1, Examined: 1_025, PathBytes: 1, PageBytes: 1, TotalBytes: 1, SourceRefs: 1, RoundPages: 1},
		{Returned: 1, Examined: 1, PathBytes: 2_049, PageBytes: 1, TotalBytes: 1, SourceRefs: 1, RoundPages: 1},
		{Returned: 1, Examined: 1, PathBytes: 1, PageBytes: 4<<20 + 1, TotalBytes: 4 << 20, SourceRefs: 1, RoundPages: 1},
		{Returned: 1, Examined: 1, PathBytes: 1, PageBytes: 1, TotalBytes: 4<<20 + 1, SourceRefs: 1, RoundPages: 1},
		{Returned: 1, Examined: 1, PathBytes: 1, PageBytes: 1, TotalBytes: 1, SourceRefs: 65, RoundPages: 1},
		{Returned: 1, Examined: 1, PathBytes: 1, PageBytes: 1, TotalBytes: 1, SourceRefs: 1, RoundPages: 100_001},
		{Returned: 0, Examined: 1, PathBytes: 1, PageBytes: 1, TotalBytes: 1, SourceRefs: 1, RoundPages: 1},
	} {
		if _, err := app.NormalizeCanonicalProvenanceLimits(limits); !errors.Is(err, app.ErrCanonicalProvenanceInvalid) {
			t.Errorf("NormalizeCanonicalProvenanceLimits(%#v) error = %v", limits, err)
		}
	}
}
