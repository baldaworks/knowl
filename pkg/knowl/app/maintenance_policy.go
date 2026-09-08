package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// SourceMaintenanceContractVersion identifies the output-affecting maintainer
// and validation contract. Compatibility-only changes must retain this value.
const SourceMaintenanceContractVersion = "source-maintenance-v1"

const maintenanceGenerationPrefixBytes = 16

// MaintenancePolicy is the explicit non-secret input to source-maintenance
// generation. It intentionally cannot carry provider or runtime configuration.
type MaintenancePolicy struct {
	ContractVersion string
	SchemaDigest    string
	ReadLimits      knowl.ReadLimits
	PlanLimits      PlanLimits
}

type maintenancePolicyPayload struct {
	ContractVersion string                `json:"contract_version"`
	SchemaDigest    string                `json:"schema_digest"`
	ReadLimits      maintenanceReadLimits `json:"read_limits"`
	PlanLimits      maintenancePlanLimits `json:"plan_limits"`
}

type maintenanceReadLimits struct {
	Pages         int   `json:"pages"`
	Bytes         int   `json:"bytes"`
	Characters    int   `json:"characters"`
	Depth         int   `json:"depth"`
	DeadlineNanos int64 `json:"deadline_nanos"`
}

type maintenancePlanLimits struct {
	MaxFiles         int `json:"max_files"`
	MaxFileBytes     int `json:"max_file_bytes"`
	MaxSourceRefs    int `json:"max_source_refs"`
	MaxRationaleSize int `json:"max_rationale_size"`
}

// SourceMaintenancePolicy constructs the effective policy used by ingestion.
func SourceMaintenancePolicy(schemaDigest string, readLimits knowl.ReadLimits, planLimits PlanLimits) MaintenancePolicy {
	return MaintenancePolicy{
		ContractVersion: SourceMaintenanceContractVersion,
		SchemaDigest:    schemaDigest,
		ReadLimits:      readLimits,
		PlanLimits:      planLimits,
	}
}

// MaintenancePolicyGeneration returns the full lowercase SHA-256 digest of a
// canonical, allow-listed policy payload.
func MaintenancePolicyGeneration(policy MaintenancePolicy) (string, error) {
	payload, err := normalizeMaintenancePolicy(policy)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode maintenance policy: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidateMaintenanceGeneration accepts the legacy empty sentinel or one
// canonical full policy digest.
func ValidateMaintenanceGeneration(generation string) error {
	if !validOptionalGeneration(generation) {
		return ErrExecutionDescriptorUnavailable
	}
	return nil
}

// MaintenanceGenerationPrefix returns the bounded operator-facing portion of
// a generation while retaining the empty legacy sentinel.
func MaintenanceGenerationPrefix(generation string) (string, error) {
	if err := ValidateMaintenanceGeneration(generation); err != nil {
		return "", err
	}
	if generation == "" {
		return "", nil
	}
	return generation[:maintenanceGenerationPrefixBytes], nil
}

func normalizeMaintenancePolicy(policy MaintenancePolicy) (maintenancePolicyPayload, error) {
	contractVersion := strings.TrimSpace(policy.ContractVersion)
	schemaDigest := strings.ToLower(strings.TrimSpace(policy.SchemaDigest))
	readLimits := policy.ReadLimits
	planLimits := policy.PlanLimits
	if readLimits == (knowl.ReadLimits{}) {
		readLimits = DefaultReadLimits()
	}
	if planLimits == (PlanLimits{}) {
		planLimits = DefaultPlanLimits()
	}
	if !validStoredText(contractVersion, maxPlannerVersionBytes, false) || !validExecutionDigest(schemaDigest) ||
		readLimits.Pages <= 0 || readLimits.Bytes <= 0 || readLimits.Characters <= 0 || readLimits.Depth <= 0 ||
		readLimits.Deadline <= 0 ||
		planLimits.MaxFiles <= 0 || planLimits.MaxFileBytes <= 0 || planLimits.MaxSourceRefs <= 0 || planLimits.MaxRationaleSize <= 0 {
		return maintenancePolicyPayload{}, fmt.Errorf("invalid maintenance policy: %w", ErrExecutionDescriptorUnavailable)
	}
	return maintenancePolicyPayload{
		ContractVersion: contractVersion,
		SchemaDigest:    schemaDigest,
		ReadLimits: maintenanceReadLimits{
			Pages: readLimits.Pages, Bytes: readLimits.Bytes, Characters: readLimits.Characters,
			Depth: readLimits.Depth, DeadlineNanos: int64(readLimits.Deadline),
		},
		PlanLimits: maintenancePlanLimits(planLimits),
	}, nil
}
