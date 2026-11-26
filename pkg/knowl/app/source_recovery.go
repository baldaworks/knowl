package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const RetirementRecoveryInvalidProofCode = "retirement.recovery_invalid_proof"

// SourceLifecycleRecoveryStore is the optional durable capability used by the
// normal coordinator to discover and retire proved historical owners.
type SourceLifecycleRecoveryStore interface {
	OrphanSourceOperations(ctx context.Context, request OrphanSourceOperationReadRequest) (OrphanSourceOperationPage, error)
	RecoverSourceLifecycle(ctx context.Context, request SourceLifecycleRecoveryRequest) (knowl.SourceLifecycle, error)
}

type OrphanSourceOperationReadRequest struct {
	Scope knowl.ScopeRef
	After knowl.OperationID
	Limit int
}

// Invalid candidates retain their operation key so bounded observation can
// advance past corrupt historical metadata without silently granting authority.
type OrphanSourceOperation struct {
	OperationID knowl.OperationID
	Owner       knowl.SourceOwner
	Source      knowl.AcceptedSource
	Invalid     bool
}

type OrphanSourceOperationPage struct {
	Items []OrphanSourceOperation
	Next  knowl.OperationID
}

// SourceLifecycleRecoveryRequest carries proof authenticated by the host under
// its shared apply fence. The coordinator verifies the current canonical page
// and original raw bytes; the transaction verifies the exact durable operation.
// It is not an active source configuration or a claim about historical config.
type SourceLifecycleRecoveryRequest struct {
	Scope               knowl.ScopeRef
	Owner               knowl.SourceOwner
	AuthorityGeneration string
	AuthorityEpoch      uint64
	OperationID         knowl.OperationID
	Source              knowl.AcceptedSource
	PageID              knowl.PageID
	PageDigest          string
	RecoveredAt         time.Time
}

func ValidateOrphanSourceOperationRead(request OrphanSourceOperationReadRequest) error {
	if strings.TrimSpace(string(request.Scope)) == "" || request.Limit < 1 || request.Limit > 100 ||
		!validRecoveryOperationID(request.After, true) {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

func validRecoveryOperationID(id knowl.OperationID, empty bool) bool {
	return (empty || id != "") && len(id) <= 256 && strings.TrimSpace(string(id)) == string(id) && !strings.ContainsAny(string(id), "\x00\r\n")
}

func ValidateSourceLifecycleRecovery(request SourceLifecycleRecoveryRequest) error {
	if strings.TrimSpace(string(request.Scope)) == "" || ValidateSourceOwner(request.Owner) != nil ||
		!validSHA256(request.AuthorityGeneration) || request.AuthorityEpoch == 0 || request.AuthorityEpoch > math.MaxInt64 ||
		!validRecoveryOperationID(request.OperationID, false) || ValidateAcceptedSourceDescriptor(request.Source) != nil ||
		request.Source.Scope != request.Scope || knowl.SourceAdapterID(request.Source.Source.Adapter) != request.Owner.Adapter ||
		request.Source.SourceDocument.SourceID != request.Owner.SourceID || !validRetirementPageID(request.PageID) || !validSHA256(request.PageDigest) || request.RecoveredAt.IsZero() {
		return ErrSourceLifecycleInvalid
	}
	if path, err := validateEditPath("wiki/" + string(request.PageID) + ".md"); err != nil || path != "wiki/"+string(request.PageID)+".md" {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

// Recovery proof contains only bounded metadata, never source/page text.
const MaxSourceLifecycleRecoveryProofBytes = 64 << 10

type sourceLifecycleRecoveryProof struct {
	Request SourceLifecycleRecoveryRequest `json:"request"`
	Digest  string                         `json:"digest"`
}

// EncodeSourceLifecycleRecoveryProof keeps the exact private audit preimage
// alongside its public digest. Observation time does not change identity.
func EncodeSourceLifecycleRecoveryProof(request SourceLifecycleRecoveryRequest) (string, string, error) {
	if err := ValidateSourceLifecycleRecovery(request); err != nil {
		return "", "", err
	}
	request.RecoveredAt = time.Time{}
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", "", ErrSourceLifecycleInvalid
	}
	hash := sha256.Sum256(append([]byte("knowl-source-retirement-recovery-v1\x00"), encoded...))
	digest := hex.EncodeToString(hash[:])
	payload, err := json.Marshal(sourceLifecycleRecoveryProof{Request: request, Digest: digest})
	if err != nil || len(payload) > MaxSourceLifecycleRecoveryProofBytes {
		return "", "", ErrSourceLifecycleInvalid
	}
	return string(payload), digest, nil
}

func SourceLifecycleRecoveryProofDigest(request SourceLifecycleRecoveryRequest) (string, error) {
	_, digest, err := EncodeSourceLifecycleRecoveryProof(request)
	return digest, err
}

// ValidateSourceLifecycleRecoveryPayload authenticates the private audit
// metadata when decoding a row, including after it acquires a real config.
// Historical payload never grants active authority.
func ValidateSourceLifecycleRecoveryPayload(state knowl.SourceLifecycle, payload string) error {
	if payload == "" {
		if state.RecoveryProofDigest != "" {
			return ErrSourceLifecycleInvalid
		}
		return nil
	}
	if len(payload) > MaxSourceLifecycleRecoveryProofBytes {
		return ErrSourceLifecycleInvalid
	}
	var proof sourceLifecycleRecoveryProof
	if json.Unmarshal([]byte(payload), &proof) != nil || !proof.Request.RecoveredAt.IsZero() {
		return ErrSourceLifecycleInvalid
	}
	proof.Request.RecoveredAt = state.ActivatedAt
	canonical, digest, err := EncodeSourceLifecycleRecoveryProof(proof.Request)
	if err != nil || canonical != payload || proof.Request.Scope != state.Scope || proof.Request.Owner != state.Owner ||
		state.RecoveryProofDigest != "" && state.RecoveryProofDigest != digest {
		return ErrSourceLifecycleInvalid
	}
	return nil
}

// ValidateRetirementOwnerBinding keeps genuine config identity and recovery
// identity distinct. Active owner validation adds its stricter config-only rule.
func ValidateRetirementOwnerBinding(config, proof string) error {
	if config != "" && proof == "" && validSHA256(config) || config == "" && validSHA256(proof) {
		return nil
	}
	return ErrSourceLifecycleInvalid
}
