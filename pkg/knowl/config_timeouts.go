package knowl

import (
	"errors"
	"math"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

// ErrTimeoutConfigInvalid identifies an invalid operational execution budget.
var ErrTimeoutConfigInvalid = errors.New("invalid operational timeout configuration")

// TimeoutsConfig controls operational budgets, independently of model planning
// limits and immutable maintenance or embedding identities. Zero selects defaults.
type TimeoutsConfig struct {
	ProjectionRebuild time.Duration `mapstructure:"projection_rebuild"`
	Hierarchy         time.Duration `mapstructure:"hierarchy"`
}

// Normalize resolves positive budgets without starting any work. By default the
// outer hierarchy budget includes both model planning and a full projection build.
func (config TimeoutsConfig) Normalize() (TimeoutsConfig, error) {
	if config.ProjectionRebuild < 0 || config.Hierarchy < 0 {
		return TimeoutsConfig{}, ErrTimeoutConfigInvalid
	}
	if config.ProjectionRebuild == 0 {
		config.ProjectionRebuild = app.DefaultProjectionRebuildTimeout
	}
	if config.Hierarchy == 0 {
		if config.ProjectionRebuild > time.Duration(math.MaxInt64)-app.MaxCorrectionDeadline {
			return TimeoutsConfig{}, ErrTimeoutConfigInvalid
		}
		config.Hierarchy = config.ProjectionRebuild + app.MaxCorrectionDeadline
	}
	return config, nil
}
