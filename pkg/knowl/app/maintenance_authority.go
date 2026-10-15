package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/baldaworks/knowl/pkg/knowl/internal/maintenanceauthority"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

var ErrMaintenanceAuthorityRejected = errors.New("maintenance source authority rejected")

const maintenancePlanDiagnosticPath = "<plan>"

// MaintenanceAuthorityError exposes bounded page/owner diagnostics without
// leaking lifecycle-store errors or source content.
type MaintenanceAuthorityError struct{ Diagnostics []knowl.MaintenanceDiagnostic }

func (err *MaintenanceAuthorityError) Error() string { return err.SafeDetail() }
func (err *MaintenanceAuthorityError) Unwrap() error { return ErrMaintenanceAuthorityRejected }
func (err *MaintenanceAuthorityError) SafeDetail() string {
	first := err.Diagnostics[0]
	return fmt.Sprintf("content validation failed for %q (%s)", first.Path, first.Code)
}
func (err *MaintenanceAuthorityError) MaintenanceDiagnostics() []knowl.MaintenanceDiagnostic {
	return slices.Clone(err.Diagnostics)
}
func maintenanceAcceptedOwner(source knowl.AcceptedSource) (knowl.SourceOwner, bool) {
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterID(source.Source.Adapter), SourceID: source.SourceDocument.SourceID}
	return owner, ValidateSourceOwner(owner) == nil
}

// ValidateMaintenanceAuthority authenticates exact configured owners against
// active lifecycle authority. Revalidation compares only applicable owner
// configuration/activation stamps, so unrelated inventory changes remain safe.
func ValidateMaintenanceAuthority(ctx context.Context, reader SourceLifecycleReader, content RetirementContentReader, scope knowl.ScopeRef, edits []knowl.FileEdit, declared []string, expected *maintenanceauthority.Stamp) (*maintenanceauthority.Stamp, []knowl.MaintenanceDiagnostic, error) {
	authority, err := reader.SourceLifecycleAuthority(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	if authority.Scope != scope || authority.Epoch == 0 || authority.Generation == "" {
		return nil, nil, ErrSourceLifecycleConflict
	}
	if expected != nil && (expected.Scope != scope || expected.Epoch == 0 || !validSHA256(expected.Generation)) {
		return nil, nil, ErrSourceLifecycleConflict
	}
	refs := slices.Clone(declared)
	for _, edit := range edits {
		metadata, parseErr := knowlwiki.ParseFrontmatter(string(edit.Content))
		if parseErr == nil {
			refs = append(refs, metadata.SourceRefs...)
		}
	}
	slices.Sort(refs)
	refs = slices.Compact(refs)
	refs = slices.DeleteFunc(refs, func(ref string) bool {
		candidates, err := ParseSourceRefCandidates(ref)
		if err != nil {
			return true
		} // Existing filesystem citation validation owns malformed refs.
		adapter := candidates[0].Source.Adapter
		return adapter != string(knowl.SourceAdapterFilesystem) && adapter != string(knowl.SourceAdapterGit)
	})
	var raw map[string]knowl.AcceptedSource
	if len(refs) > 0 {
		raw, err = content.ResolveAcceptedSources(ctx, scope, refs)
	}
	if err != nil {
		var missing *SourceRefNotFoundError
		if errors.As(err, &missing) {
			path := maintenancePlanDiagnosticPath
			for _, edit := range edits {
				metadata, parseErr := knowlwiki.ParseFrontmatter(string(edit.Content))
				if parseErr == nil && slices.Contains(metadata.SourceRefs, missing.Ref) {
					path = edit.Path
					break
				}
			}
			if path == maintenancePlanDiagnosticPath && len(edits) > 0 {
				path = edits[0].Path
			}
			diagnostics := []knowl.MaintenanceDiagnostic{{Code: knowl.DiagnosticCitationUnknownSource, Path: path, Target: missing.Ref}}
			return nil, diagnostics, &MaintenanceAuthorityError{Diagnostics: diagnostics}
		}
		return nil, nil, err
	}
	states := make(map[knowl.SourceOwner]knowl.SourceLifecycle)
	var diagnostics []knowl.MaintenanceDiagnostic
	check := func(path, ref string) error {
		source, found := raw[ref]
		if !found {
			return nil
		} // Existing citation validation owns unknown refs.
		// Only configured adapters have a source-retirement lifecycle. Inline
		// facts and application adapters keep their existing accepted-evidence
		// authority; they cannot be removed from configured source inventory.
		if source.Source.Adapter != string(knowl.SourceAdapterFilesystem) && source.Source.Adapter != string(knowl.SourceAdapterGit) {
			return nil
		}
		owner, owned := maintenanceAcceptedOwner(source)
		target := string(owner.Adapter) + ":" + string(owner.SourceID)
		code := SourceLifecycleUnownedCode
		if owned {
			state, cached := states[owner]
			var readErr error
			if !cached {
				state, readErr = reader.SourceLifecycle(ctx, scope, owner)
				if readErr != nil && !errors.Is(readErr, ErrSourceLifecycleUnowned) {
					return readErr
				}
				if readErr == nil {
					states[owner] = state
				}
			}
			if readErr == nil && state.Scope == scope && state.Owner == owner && state.State == knowl.SourceLifecycleActive && state.AuthorityEpoch == authority.Epoch && state.AuthorityGeneration == authority.Generation {
				if expected == nil || slices.Contains(expected.Owners, maintenanceauthority.Owner{Owner: owner, ConfigDigest: state.ConfigDigest, ActivatedEpoch: state.ActivatedEpoch}) {
					return nil
				}
				code = CitationSourceAuthorityChangedCode
			}
			if state.State == knowl.SourceLifecycleRetiring || state.State == knowl.SourceLifecycleDeleted {
				code = CitationSourceRetiringCode
			}
		}
		diagnostics = append(diagnostics, knowl.MaintenanceDiagnostic{Path: path, Code: code, Target: target})
		return nil
	}
	for _, edit := range edits {
		if edit.Action == knowl.SourceMutationDelete {
			continue
		}
		metadata, parseErr := knowlwiki.ParseFrontmatter(string(edit.Content))
		if parseErr == nil {
			for _, ref := range metadata.SourceRefs {
				if err := check(edit.Path, ref); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	for _, ref := range declared {
		path := maintenancePlanDiagnosticPath
		if len(edits) > 0 {
			path = edits[0].Path
		}
		if err := check(path, ref); err != nil {
			return nil, nil, err
		}
	}
	diagnostics, err = NormalizeMaintenanceDiagnostics(diagnostics)
	if err != nil {
		return nil, nil, err
	}
	if len(diagnostics) > 0 {
		return nil, diagnostics, &MaintenanceAuthorityError{Diagnostics: diagnostics}
	}
	snapshot := &maintenanceauthority.Stamp{Scope: scope, Generation: authority.Generation, Epoch: authority.Epoch}
	for owner, state := range states {
		snapshot.Owners = append(snapshot.Owners, maintenanceauthority.Owner{Owner: owner, ConfigDigest: state.ConfigDigest, ActivatedEpoch: state.ActivatedEpoch})
	}
	slices.SortFunc(snapshot.Owners, func(a, b maintenanceauthority.Owner) int {
		if order := cmp.Compare(a.Owner.Adapter, b.Owner.Adapter); order != 0 {
			return order
		}
		return cmp.Compare(a.Owner.SourceID, b.Owner.SourceID)
	})
	return snapshot, nil, nil
}
