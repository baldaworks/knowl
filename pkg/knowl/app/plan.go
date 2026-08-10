package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

var (
	ErrPlanInvalid       = errors.New("invalid maintainer edit plan")
	ErrSchemaMismatch    = errors.New("edit plan schema digest mismatch")
	ErrForbiddenEdit     = errors.New("edit plan targets a forbidden path")
	ErrPlanLimitExceeded = errors.New("edit plan exceeds a limit")
)

// PlanLimits bounds model output before it reaches a content adapter.
type PlanLimits struct {
	MaxFiles         int
	MaxFileBytes     int
	MaxSourceRefs    int
	MaxRationaleSize int
}

// DefaultPlanLimits returns conservative local plan limits.
func DefaultPlanLimits() PlanLimits {
	return PlanLimits{MaxFiles: 32, MaxFileBytes: 256 << 10, MaxSourceRefs: 64, MaxRationaleSize: 32 << 10}
}

// ValidatePlan turns provider data into an application-owned plan.
func ValidatePlan(ctx context.Context, input knowl.MaintenanceInput, plan knowl.ModelEditPlan, limits PlanLimits) (knowl.ValidatedEditPlan, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.ValidatedEditPlan{}, err
	}
	if limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxSourceRefs <= 0 || limits.MaxRationaleSize <= 0 {
		return knowl.ValidatedEditPlan{}, fmt.Errorf("invalid plan limits: %w", ErrPlanInvalid)
	}
	if strings.TrimSpace(input.Schema.Digest) == "" || plan.SchemaDigest != input.Schema.Digest {
		return knowl.ValidatedEditPlan{}, ErrSchemaMismatch
	}
	if len(plan.Edits) == 0 || len(plan.Edits) > limits.MaxFiles {
		return knowl.ValidatedEditPlan{}, fmt.Errorf("plan file count %d: %w", len(plan.Edits), ErrPlanLimitExceeded)
	}
	if len(plan.SourceRefs) == 0 || len(plan.SourceRefs) > limits.MaxSourceRefs {
		return knowl.ValidatedEditPlan{}, fmt.Errorf("plan source references: %w", ErrPlanInvalid)
	}
	if len(plan.Rationale) > limits.MaxRationaleSize {
		return knowl.ValidatedEditPlan{}, fmt.Errorf("plan rationale: %w", ErrPlanLimitExceeded)
	}
	wantedSource := SourceRefKey(input.Source)
	hasSource := false
	sourceRefs := append([]string(nil), plan.SourceRefs...)
	sort.Strings(sourceRefs)
	for _, sourceRef := range sourceRefs {
		if strings.TrimSpace(sourceRef) == wantedSource {
			hasSource = true
		}
	}
	if !hasSource {
		return knowl.ValidatedEditPlan{}, fmt.Errorf("plan does not cite accepted source %q: %w", wantedSource, ErrPlanInvalid)
	}

	edits := make([]knowl.FileEdit, len(plan.Edits))
	seen := make(map[string]struct{}, len(plan.Edits))
	for index, edit := range plan.Edits {
		path, err := validateEditPath(edit.Path)
		if err != nil {
			return knowl.ValidatedEditPlan{}, err
		}
		if _, exists := seen[path]; exists {
			return knowl.ValidatedEditPlan{}, fmt.Errorf("duplicate path %q: %w", path, ErrPlanInvalid)
		}
		seen[path] = struct{}{}
		if len(edit.Content) > limits.MaxFileBytes {
			return knowl.ValidatedEditPlan{}, fmt.Errorf("file %q: %w", path, ErrPlanLimitExceeded)
		}
		edit.Path = path
		edit.Content = append([]byte(nil), edit.Content...)
		edits[index] = edit
	}
	sort.Slice(edits, func(left, right int) bool { return edits[left].Path < edits[right].Path })
	return knowl.ValidatedEditPlan{
		OperationID:  planOperationID(input),
		Scope:        input.Scope,
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   sourceRefs,
		Edits:        edits,
	}, nil
}

// SourceRefKey is the stable citation key required in a plan.
func SourceRefKey(source knowl.AcceptedSource) string {
	return source.Source.Adapter + ":" + source.Source.ID + "@" + source.Version.Version
}

// PlanDigest returns a deterministic digest of a validated plan.
func PlanDigest(plan knowl.ValidatedEditPlan) (string, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("marshal validated plan: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func planOperationID(input knowl.MaintenanceInput) string {
	return string(input.Scope) + ":" + SourceRefKey(input.Source)
}

func validateEditPath(raw string) (string, error) {
	path := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
	if path == "." || filepath.IsAbs(path) || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") || !strings.HasPrefix(path, "wiki/") || path == "wiki/log.md" || filepath.Ext(path) != ".md" {
		return "", fmt.Errorf("edit path %q: %w", raw, ErrForbiddenEdit)
	}
	return path, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
