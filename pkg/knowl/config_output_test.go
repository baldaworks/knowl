package knowl

import (
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func TestConfigValidateRejectsUnboundedOutputBeforeStartup(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	for _, value := range []int{-1, 2} {
		config.Output.MaxCorrections = &value
		if err := config.Validate(); !errors.Is(err, app.ErrOutputCorrectionInvalid) {
			t.Fatalf("invalid allowance passed preflight: %v", err)
		}
	}
	zero := 0
	config.Output.MaxCorrections = &zero
	if err := config.Validate(); err != nil {
		t.Fatalf("explicit disable rejected: %v", err)
	}
}
