package knowl

import "fmt"

// MigrationDirection selects a schema-only operation.
type MigrationDirection string

const (
	MigrationUp   MigrationDirection = "up"
	MigrationDown MigrationDirection = "down"
)

// MigrationStep identifies an actually executed migration in its released history.
type MigrationStep struct {
	Stream  string `json:"stream"`
	Version int64  `json:"version"`
}

// MigrationResult contains no connection configuration or provider state.
type MigrationResult struct {
	Backend   string             `json:"backend"`
	Direction MigrationDirection `json:"direction"`
	Steps     []MigrationStep    `json:"steps"`
}

// MigrationStage identifies a safe, stable failure boundary.
type MigrationStage string

const (
	MigrationStageConfiguration MigrationStage = "configuration"
	MigrationStageConnection    MigrationStage = "connection"
	MigrationStageHistory       MigrationStage = "history"
	MigrationStageApply         MigrationStage = "apply"
	MigrationStageValidation    MigrationStage = "validation"
	MigrationStageLock          MigrationStage = "lock"
)

// MigrationError keeps unsafe driver details available only through Unwrap.
// Its public rendering includes only recognized metadata, never the cause.
type MigrationError struct {
	Stage    MigrationStage
	Stream   string
	Version  int64
	SQLState string
	Cause    error
}

func (failure *MigrationError) Error() string {
	stage := failure.Stage
	switch stage {
	case MigrationStageConfiguration, MigrationStageConnection, MigrationStageHistory,
		MigrationStageApply, MigrationStageValidation, MigrationStageLock:
	default:
		stage = "unknown"
	}
	stream := failure.Stream
	if stream != "base" && stream != "pgvector" {
		stream = "unknown"
	}
	metadata := fmt.Sprintf("stage=%s stream=%s version=%d", stage, stream, failure.Version)
	if safeSQLState(failure.SQLState) {
		metadata += " sqlstate=" + failure.SQLState
	}
	return "database migration failed (" + metadata + ")"
}

func safeSQLState(state string) bool {
	if len(state) != 5 {
		return false
	}
	for _, character := range state {
		if (character < '0' || character > '9') && (character < 'A' || character > 'Z') {
			return false
		}
	}
	return true
}

func (failure *MigrationError) Unwrap() error { return failure.Cause }
