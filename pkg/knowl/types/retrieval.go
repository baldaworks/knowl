package knowl

// RetrievalMode describes actual read behavior, not configured capabilities.
type RetrievalMode string

const (
	RetrievalLexical  RetrievalMode = "lexical"
	RetrievalHybrid   RetrievalMode = "hybrid"
	RetrievalDegraded RetrievalMode = "degraded"
	RetrievalFailed   RetrievalMode = "failed"
)

// RetrievalFailure is an allowlisted reason safe for durable/public reports.
type RetrievalFailure string

const (
	RetrievalInvalidConfiguration RetrievalFailure = "invalid_configuration"
	RetrievalInvalidInput         RetrievalFailure = "invalid_input"
	RetrievalUnavailable          RetrievalFailure = "unavailable"
	RetrievalDeadline             RetrievalFailure = "deadline"
	RetrievalInputLimit           RetrievalFailure = "input_limit"
	RetrievalResponseLimit        RetrievalFailure = "response_limit"
	RetrievalInvalidResponse      RetrievalFailure = "invalid_response"
	RetrievalModelMismatch        RetrievalFailure = "model_mismatch"
	RetrievalDimensionMismatch    RetrievalFailure = "dimension_mismatch"
	RetrievalProjectionNotReady   RetrievalFailure = "projection_not_ready"
	RetrievalProjectionDrift      RetrievalFailure = "projection_drift"
	RetrievalProjectionCapacity   RetrievalFailure = "projection_capacity"
)

// RetrievalReport is per-call bounded evidence. It contains no source/query text,
// endpoint URLs, credentials or upstream bodies. Missing legacy reports stay nil.
type RetrievalReport struct {
	Requested          RetrievalMode    `json:"requested"`
	Effective          RetrievalMode    `json:"effective"`
	Reason             RetrievalFailure `json:"reason,omitempty"`
	ModelSpace         string           `json:"model_space,omitempty"`
	LexicalCandidates  int              `json:"lexical_candidates"`
	VectorCandidates   int              `json:"vector_candidates"`
	FusedCandidates    int              `json:"fused_candidates"`
	ScannedChunks      int              `json:"scanned_chunks"`
	QueryOmittedRunes  int              `json:"query_omitted_runes"`
	IndexOmittedChunks int              `json:"index_omitted_chunks"`
	IndexOmittedRunes  int              `json:"index_omitted_runes"`
}

// RetrievalStatus is the public, safe summary of a retrieval attempt.
type RetrievalStatus struct {
	Effective RetrievalMode    `json:"effective"`
	Reason    RetrievalFailure `json:"reason,omitempty"`
}
