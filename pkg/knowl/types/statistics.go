package knowl

import "time"

// StatisticsAvailability describes whether an optional physical metric is
// available without exposing backend error details.
type StatisticsAvailability string

// ProjectionStatisticsStatus is the validated state of a statistics
// projection. Not every status applies to both projection kinds.
type ProjectionStatisticsStatus string

const (
	StatisticsAvailable              StatisticsAvailability = "available"
	StatisticsUnsupported            StatisticsAvailability = "unsupported"
	StatisticsPermissionDenied       StatisticsAvailability = "permission_denied"
	StatisticsTemporarilyUnavailable StatisticsAvailability = "temporarily_unavailable"
)

const (
	ProjectionStatisticsDisabled    ProjectionStatisticsStatus = "disabled"
	ProjectionStatisticsUnavailable ProjectionStatisticsStatus = "unavailable"
	ProjectionStatisticsReady       ProjectionStatisticsStatus = "ready"
	ProjectionStatisticsIncomplete  ProjectionStatisticsStatus = "incomplete"
	ProjectionStatisticsInvalid     ProjectionStatisticsStatus = "invalid"
)

const (
	StatisticsBackendSQLite   = "sqlite"
	StatisticsBackendPostgres = "postgres"
)

const (
	StatisticsTableOperations      = "operations"
	StatisticsTablePages           = "pages"
	StatisticsTableLinks           = "links"
	StatisticsTableProjectionState = "projection_state"
	StatisticsTableSources         = "sources"
	StatisticsTableSourceDocuments = "source_documents"
	StatisticsTablePageSources     = "page_sources"
	StatisticsTableEmbeddingState  = "embedding_state"
	StatisticsTableEmbeddingChunks = "embedding_chunks"
)

// StatisticsMetric is an optional non-negative integer metric.
type StatisticsMetric struct {
	Availability StatisticsAvailability `json:"availability"`
	Value        int64                  `json:"value,omitempty"`
}

// ProductStatistics contains logical facts for the configured scope.
type ProductStatistics struct {
	Pages              int64                     `json:"pages"`
	Links              int64                     `json:"links"`
	ConfiguredSources  int64                     `json:"configured_sources"`
	SourceDocuments    int64                     `json:"source_documents"`
	OperationsTotal    int64                     `json:"operations_total"`
	OperationsByStatus map[OperationStatus]int64 `json:"operations_by_status"`
}

// ProjectionStatistics describes current lexical and embedding coverage.
type ProjectionStatistics struct {
	LexicalStatus   ProjectionStatisticsStatus `json:"lexical_status"`
	EmbeddingStatus ProjectionStatisticsStatus `json:"embedding_status"`
	EligiblePages   int64                      `json:"eligible_pages"`
	EmbeddedPages   int64                      `json:"embedded_pages"`
	Chunks          int64                      `json:"chunks"`
}

// TableStatistics contains deployment-wide facts for one fixed Knowl table.
type TableStatistics struct {
	Name       string           `json:"name"`
	Rows       int64            `json:"rows"`
	DataBytes  StatisticsMetric `json:"data_bytes"`
	IndexBytes StatisticsMetric `json:"index_bytes"`
	TotalBytes StatisticsMetric `json:"total_bytes"`
}

// StorageStatistics contains deployment-wide physical storage facts.
type StorageStatistics struct {
	Backend       string            `json:"backend"`
	DatabaseBytes StatisticsMetric  `json:"database_bytes"`
	Tables        []TableStatistics `json:"tables"`
}

// OperatorStatistics is one detached current read snapshot. ObservedAt is
// captured when the backend read transaction starts; the snapshot is not
// persisted.
type OperatorStatistics struct {
	ObservedAt time.Time            `json:"observed_at"`
	Product    ProductStatistics    `json:"product"`
	Projection ProjectionStatistics `json:"projection"`
	Storage    StorageStatistics    `json:"storage"`
}
