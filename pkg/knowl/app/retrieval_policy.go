package app

import (
	"encoding/hex"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// MaintenanceRetrievalPolicy contains only output-affecting retrieval choices.
// Endpoints, credentials, inference timeout/concurrency and deployment cannot
// enter maintenance identity through this allowlisted shape.
type MaintenanceRetrievalPolicy struct {
	Mode                knowl.RetrievalMode    `json:"mode"`
	ModelSpace          string                 `json:"model_space"`
	Preprocessing       string                 `json:"preprocessing"`
	VectorNormalization string                 `json:"vector_normalization"`
	Fusion              string                 `json:"fusion"`
	RankConstant        int                    `json:"rank_constant"`
	MinimumCandidates   int                    `json:"minimum_candidates"`
	MaximumCandidates   int                    `json:"maximum_candidates"`
	CandidateMultiplier int                    `json:"candidate_multiplier"`
	MaxChunks           int                    `json:"max_chunks"`
	MaxProjectionBytes  int                    `json:"max_projection_bytes"`
	FailurePolicy       EmbeddingFailurePolicy `json:"failure_policy"`
}

type MaintenanceRetrievalPolicyProvider interface {
	MaintenanceRetrievalPolicy() *MaintenanceRetrievalPolicy
}

func validateMaintenanceRetrievalPolicy(policy *MaintenanceRetrievalPolicy) error {
	if policy == nil {
		return nil
	}
	if policy.Mode != knowl.RetrievalHybrid || len(policy.ModelSpace) != 64 || strings.ToLower(policy.ModelSpace) != policy.ModelSpace {
		return ErrExecutionDescriptorUnavailable
	}
	if _, err := hex.DecodeString(policy.ModelSpace); err != nil {
		return ErrExecutionDescriptorUnavailable
	}
	for _, value := range []string{policy.Preprocessing, policy.VectorNormalization, policy.Fusion} {
		if !validStoredText(value, 256, false) {
			return ErrExecutionDescriptorUnavailable
		}
	}
	if policy.RankConstant <= 0 || policy.MinimumCandidates <= 0 || policy.MaximumCandidates < policy.MinimumCandidates || policy.CandidateMultiplier <= 0 || policy.MaxChunks <= 0 || policy.MaxProjectionBytes <= 0 || (policy.FailurePolicy != EmbeddingFallbackLexical && policy.FailurePolicy != EmbeddingStrict) {
		return ErrExecutionDescriptorUnavailable
	}
	return nil
}
