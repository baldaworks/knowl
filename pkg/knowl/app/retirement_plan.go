package app

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const (
	// RetirementMaintenanceContractVersion identifies the page-scoped provider contract.
	RetirementMaintenanceContractVersion = "source-retirement-v1"
	// MaxRetirementEvidenceBytes is the aggregate raw active-evidence ceiling.
	MaxRetirementEvidenceBytes = 4 << 20
	maxRetirementEvidenceRefs  = 64
	maxRetirementPageRefs      = MaxAcceptedSourceResolveRefs

	CitationPlanMissingCode             = "citation.plan_missing"
	CitationSourceRetiringCode          = "citation.source_retiring"
	RetirementMaintainerUnavailableCode = "retirement.maintainer_unavailable"
	RetirementMaintainerFailedCode      = "retirement.maintainer_failed"
	RetirementLifecycleConflictCode     = "retirement.lifecycle_conflict"
	RetirementPageStaleCode             = "retirement.page_stale"
	RetirementEvidenceLimitCode         = "retirement.evidence_limit"
	RetirementEvidenceUnavailableCode   = "retirement.evidence_unavailable"
	RetirementEvidenceUnsupportedCode   = "retirement.evidence_unsupported"
	RetirementRequestLimitCode          = "retirement.request_limit"
	RetirementOutputLimitCode           = "retirement.output_limit"
	RetirementPageMalformedCode         = "retirement.page_malformed"
	RetirementPlanInvalidCode           = "retirement.plan_invalid"
	RetirementEvidenceInvalidCode       = "retirement.evidence_invalid"
)

var (
	ErrRetirementMaintainerUnavailable = errors.New(RetirementMaintainerUnavailableCode)
	ErrRetirementMaintainerFailed      = errors.New(RetirementMaintainerFailedCode)
	ErrRetirementLifecycleConflict     = errors.New(RetirementLifecycleConflictCode)
	ErrRetirementPageStale             = errors.New(RetirementPageStaleCode)
	ErrRetirementEvidenceLimit         = errors.New(RetirementEvidenceLimitCode)
	ErrRetirementEvidenceUnavailable   = errors.New(RetirementEvidenceUnavailableCode)
	ErrRetirementEvidenceUnsupported   = errors.New(RetirementEvidenceUnsupportedCode)
	ErrRetirementRequestLimit          = errors.New(RetirementRequestLimitCode)
	ErrRetirementOutputLimit           = errors.New(RetirementOutputLimitCode)
	ErrRetirementPlanInvalid           = errors.New(RetirementPlanInvalidCode)
)

// RetirementMaintainer is an optional, retirement-only multi-evidence provider.
// It is deliberately separate from Maintainer so ordinary source planning stays
// one-source and existing providers fail closed for mixed retirement work.
type RetirementMaintainer interface {
	PlanRetirement(ctx context.Context, input knowl.RetirementInput) (knowl.RetirementModelPlan, error)
}

// RetirementRequestSizer measures the provider's complete retirement request,
// including any custom wrapper. Implementations must be pure and finite.
type RetirementRequestSizer interface {
	RetirementRequestBudget() knowl.MaintenanceRequestBudget
	RetirementRequestBytes(ctx context.Context, input knowl.RetirementInput) (int, error)
}

// RetirementContentReader is the narrow existing content capability required
// to re-read one current page and immutable accepted evidence.
type RetirementContentReader interface {
	ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error)
	ResolveAcceptedSources(ctx context.Context, scope knowl.ScopeRef, refs []string) (map[string]knowl.AcceptedSource, error)
	ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error)
}

// SourceLifecycleReader is the read-only lifecycle capability used while planning.
type SourceLifecycleReader interface {
	SourceLifecycleAuthority(ctx context.Context, scope knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error)
	SourceLifecycle(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error)
}

// RetirementPlannerOptions contains only finite local bounds. An all-zero
// field selects the corresponding established default.
type RetirementPlannerOptions struct {
	InputLimits   knowl.MaintenanceInputLimits
	CatalogLimits knowl.CatalogLimits
	PlanLimits    PlanLimits
	ReadLimits    knowl.ReadLimits
}

// RetirementPlanningRequest binds one current canonical precondition to the
// exact source refs requested by the caller. Full accepted descriptors are
// resolved from the trusted content store before lifecycle classification.
type RetirementPlanningRequest struct {
	Scope      knowl.ScopeRef
	Page       CanonicalPageProvenance
	SourceRefs []string
	Retiring   []knowl.SourceLifecycle
	Authority  knowl.SourceLifecycleAuthority
	Schema     knowl.SchemaDocument
	Catalogs   []knowl.HierarchyCatalog
}

// RetirementPlanner creates one validated replacement/delete decision without
// staging or mutating canonical content.
type RetirementPlanner struct {
	content       RetirementContentReader
	lifecycles    SourceLifecycleReader
	maintainer    RetirementMaintainer
	requestSizer  RetirementRequestSizer
	inputLimits   knowl.MaintenanceInputLimits
	catalogLimits knowl.CatalogLimits
	planLimits    PlanLimits
	readLimits    knowl.ReadLimits
	requestBudget knowl.MaintenanceRequestBudget
}

type retirementActiveSource struct {
	ref    string
	source knowl.AcceptedSource
}

// NewRetirementPlanner constructs a bounded page-scoped retirement planner.
func NewRetirementPlanner(content RetirementContentReader, lifecycles SourceLifecycleReader, maintainer Maintainer, options RetirementPlannerOptions) (*RetirementPlanner, error) {
	if content == nil || lifecycles == nil {
		return nil, ErrRetirementPlanInvalid
	}
	inputLimits, err := NormalizeMaintenanceInputLimits(options.InputLimits)
	if err != nil {
		return nil, ErrRetirementPlanInvalid
	}
	catalogLimits, err := NormalizeCatalogLimits(options.CatalogLimits)
	if err != nil {
		return nil, ErrRetirementPlanInvalid
	}
	planLimits := options.PlanLimits
	if planLimits == (PlanLimits{}) {
		planLimits = DefaultPlanLimits()
	}
	if planLimits.MaxFiles <= 0 || planLimits.MaxFileBytes <= 0 || planLimits.MaxSourceRefs <= 0 || planLimits.MaxRationaleSize <= 0 {
		return nil, ErrRetirementPlanInvalid
	}
	readLimits := options.ReadLimits
	if readLimits == (knowl.ReadLimits{}) {
		readLimits = DefaultReadLimits()
	}
	if readLimits.Pages <= 0 || readLimits.Bytes <= 0 || readLimits.Characters <= 0 || readLimits.Depth <= 0 {
		return nil, ErrRetirementPlanInvalid
	}
	planner := &RetirementPlanner{
		content: content, lifecycles: lifecycles, inputLimits: inputLimits, catalogLimits: catalogLimits,
		planLimits: planLimits, readLimits: readLimits,
		requestBudget: knowl.MaintenanceRequestBudget{MaxBytes: inputLimits.MaxRequestBytes, FormatVersion: "retirement-json-v1"},
	}
	if retirement, ok := maintainer.(RetirementMaintainer); ok {
		planner.maintainer = retirement
		if sizer, sized := maintainer.(RetirementRequestSizer); sized {
			budget := sizer.RetirementRequestBudget()
			if budget.MaxBytes <= 0 || !validRequestFormat(budget.FormatVersion) || budget.ReservedBytes < 0 || budget.ReservedBytes >= budget.MaxBytes {
				return nil, ErrRetirementPlanInvalid
			}
			planner.requestSizer = sizer
			planner.requestBudget = budget
			planner.requestBudget.MaxBytes = min(planner.requestBudget.MaxBytes, inputLimits.MaxRequestBytes)
			if planner.requestBudget.ReservedBytes >= planner.requestBudget.MaxBytes {
				return nil, ErrRetirementPlanInvalid
			}
		}
	}
	return planner, nil
}

// PlanRetirement re-reads and classifies the page, loads only active accepted
// evidence, invokes the optional retirement provider, and validates its single
// page decision. It never stages or publishes content.
func (planner *RetirementPlanner) PlanRetirement(ctx context.Context, request RetirementPlanningRequest) (knowl.RetirementPlanResult, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.RetirementPlanResult{}, err
	}
	if err := planner.validateRequest(request); err != nil {
		return planner.failure(request.Page.Path, "", RetirementPlanInvalidCode, err)
	}
	authority, err := planner.lifecycles.SourceLifecycleAuthority(ctx, request.Scope)
	if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
		return knowl.RetirementPlanResult{}, ctxErr
	}
	if err != nil || !sameRetirementAuthority(authority, request.Authority) {
		if err == nil {
			err = ErrRetirementLifecycleConflict
		}
		return planner.failure(request.Page.Path, "", RetirementLifecycleConflictCode, errors.Join(ErrRetirementLifecycleConflict, err))
	}
	pages, err := planner.content.ReadPages(ctx, request.Scope, []knowl.PageID{request.Page.ID}, planner.readLimits)
	if err != nil {
		if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
			return knowl.RetirementPlanResult{}, ctxErr
		}
		return planner.failure(request.Page.Path, "", RetirementPageStaleCode, errors.Join(ErrRetirementPageStale, err))
	}
	if len(pages) != 1 || !sameCurrentRetirementPage(pages[0], request.Page) {
		return planner.failure(request.Page.Path, "", RetirementPageStaleCode, ErrRetirementPageStale)
	}
	page := normalizeRetirementPage(pages[0])
	metadata, err := knowlwiki.ParseFrontmatter(page.Content)
	metadataRefs, duplicateMetadataRef := normalizedUniqueRefs(metadata.SourceRefs)
	if err != nil || metadata.ID != string(page.ID) || metadata.Type == "" || page.Path != "wiki/"+string(page.ID)+".md" ||
		len(page.SourceRefs) == 0 || duplicateMetadataRef || !slices.Equal(metadataRefs, page.SourceRefs) {
		return planner.failure(page.Path, "", RetirementPageMalformedCode, ErrRetirementPlanInvalid)
	}
	if len(page.SourceRefs) > maxRetirementPageRefs {
		return planner.failure(page.Path, "", RetirementEvidenceLimitCode, ErrRetirementEvidenceLimit)
	}
	requested, err := normalizeRequestedRetirementRefs(request.SourceRefs, page.SourceRefs)
	if err != nil {
		var target string
		if failure := new(retirementRefFailure); errors.As(err, &failure) {
			target = failure.ref
		}
		return planner.failure(page.Path, target, knowl.DiagnosticCitationUnknownSource, errors.Join(ErrRetirementPlanInvalid, err))
	}
	sources, resolveErr := planner.content.ResolveAcceptedSources(ctx, request.Scope, requested)
	if ctxErr := retirementContextError(ctx, resolveErr); ctxErr != nil {
		return knowl.RetirementPlanResult{}, ctxErr
	}
	if resolveErr != nil {
		resolveFailureRef := requested[0]
		var duplicate *SourceRefDuplicateError
		if errors.As(resolveErr, &duplicate) {
			if _, found := slices.BinarySearch(requested, duplicate.Ref); found {
				resolveFailureRef = duplicate.Ref
			}
		} else {
			for _, ref := range requested {
				if _, found := sources[ref]; !found {
					resolveFailureRef = ref
					break
				}
			}
		}
		return planner.failure(page.Path, resolveFailureRef, knowl.DiagnosticCitationUnknownSource, errors.Join(ErrRetirementPlanInvalid, resolveErr))
	}
	for _, ref := range requested {
		source, found := sources[ref]
		if !found || validateResolvedRetirementSource(request.Scope, ref, source) != nil {
			return planner.failure(page.Path, ref, knowl.DiagnosticCitationUnknownSource, ErrRetirementPlanInvalid)
		}
	}
	if len(sources) != len(requested) {
		extras := make([]string, 0, len(sources))
		for ref := range sources {
			if _, requested := slices.BinarySearch(requested, ref); !requested {
				extras = append(extras, ref)
			}
		}
		sort.Strings(extras)
		target := ""
		if len(extras) > 0 {
			target = extras[0]
		}
		return planner.failure(page.Path, target, knowl.DiagnosticCitationUnknownSource, ErrRetirementPlanInvalid)
	}
	retiring, err := normalizeAuthorizedRetiring(request.Scope, request.Retiring, request.Authority)
	if err != nil {
		return planner.failure(page.Path, "", RetirementLifecycleConflictCode, errors.Join(ErrRetirementLifecycleConflict, err))
	}

	active := make([]retirementActiveSource, 0, len(page.SourceRefs))
	retiringRefs := make(map[string]struct{})
	matchedRetiring := false
	states := make(map[knowl.SourceOwner]knowl.SourceLifecycle)
	for _, ref := range page.SourceRefs {
		if err := contextErr(ctx); err != nil {
			return knowl.RetirementPlanResult{}, err
		}
		source := sources[ref]
		owner := sourceOwner(source)
		state, exists := states[owner]
		if !exists {
			state, err = planner.lifecycles.SourceLifecycle(ctx, request.Scope, owner)
			if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
				return knowl.RetirementPlanResult{}, ctxErr
			}
			if err != nil || state.Scope != request.Scope || state.Owner != owner || state.AuthorityGeneration != request.Authority.Generation || state.AuthorityEpoch != request.Authority.Epoch {
				return planner.failure(page.Path, ref, RetirementLifecycleConflictCode, errors.Join(ErrRetirementLifecycleConflict, err))
			}
			states[owner] = state
		}
		switch state.State {
		case knowl.SourceLifecycleActive:
			if !supportedRetirementMediaType(source.MediaType) {
				return planner.failure(page.Path, ref, RetirementEvidenceUnsupportedCode, ErrRetirementEvidenceUnsupported)
			}
			active = append(active, retirementActiveSource{ref: ref, source: source})
		case knowl.SourceLifecycleRetiring:
			expected, authorized := retiring[owner]
			if !authorized {
				return planner.failure(page.Path, ref, CitationSourceRetiringCode, ErrRetirementPlanInvalid)
			}
			if !sameRetiringLifecycle(state, expected) {
				return planner.failure(page.Path, ref, RetirementLifecycleConflictCode, ErrRetirementLifecycleConflict)
			}
			matchedRetiring = true
			retiringRefs[ref] = struct{}{}
		case knowl.SourceLifecycleDeleted:
			return planner.failure(page.Path, ref, CitationSourceRetiringCode, ErrRetirementPlanInvalid)
		default:
			return planner.failure(page.Path, ref, RetirementLifecycleConflictCode, ErrRetirementLifecycleConflict)
		}
	}
	if !matchedRetiring {
		return planner.failure(page.Path, "", RetirementPlanInvalidCode, ErrRetirementPlanInvalid)
	}
	if len(active) == 0 {
		if err := planner.revalidateAuthority(ctx, request); err != nil {
			if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
				return knowl.RetirementPlanResult{}, ctxErr
			}
			return planner.failure(page.Path, "", RetirementLifecycleConflictCode, err)
		}
		return retirementDeleteResult(page), nil
	}
	if len(active) > min(maxRetirementEvidenceRefs, planner.planLimits.MaxSourceRefs) {
		return planner.failure(page.Path, "", RetirementEvidenceLimitCode, ErrRetirementEvidenceLimit)
	}
	if planner.maintainer == nil {
		return planner.failure(page.Path, "", RetirementMaintainerUnavailableCode, ErrRetirementMaintainerUnavailable)
	}

	evidence := make([]knowl.RetirementEvidence, 0, len(active))
	totalBytes := 0
	for _, activeSource := range active {
		ref := activeSource.ref
		source := activeSource.source
		raw, readErr := planner.content.ReadSource(ctx, source, planner.readLimits)
		if ctxErr := retirementContextError(ctx, readErr); ctxErr != nil {
			return knowl.RetirementPlanResult{}, ctxErr
		}
		if readErr != nil {
			return planner.failure(page.Path, ref, RetirementEvidenceUnavailableCode, errors.Join(ErrRetirementEvidenceUnavailable, readErr))
		}
		if len(raw) > MaxRetirementEvidenceBytes-totalBytes {
			return planner.failure(page.Path, ref, RetirementEvidenceLimitCode, ErrRetirementEvidenceLimit)
		}
		if !supportedRetirementEvidence(source.MediaType, raw) {
			return planner.failure(page.Path, ref, RetirementEvidenceUnsupportedCode, ErrRetirementEvidenceUnsupported)
		}
		totalBytes += len(raw)
		evidence = append(evidence, knowl.RetirementEvidence{SourceRef: ref, Source: source, Text: string(raw)})
	}
	sort.Slice(evidence, func(left, right int) bool { return evidence[left].SourceRef < evidence[right].SourceRef })
	input := knowl.RetirementInput{
		ContractVersion: RetirementMaintenanceContractVersion, InputLimits: planner.inputLimits, CatalogLimits: planner.catalogLimits,
		Scope: request.Scope, Schema: cloneSchema(request.Schema), Page: clonePage(page), Evidence: cloneRetirementEvidence(evidence),
		Catalogs: normalizeRetirementCatalogs(request.Catalogs), Limits: planner.readLimits,
	}
	used, err := planner.retirementRequestBytes(ctx, input)
	if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
		return knowl.RetirementPlanResult{}, ctxErr
	}
	if err != nil {
		return planner.failure(page.Path, "", RetirementRequestLimitCode, errors.Join(ErrRetirementRequestLimit, err))
	}
	if used <= 0 || used > planner.requestBudget.MaxBytes-planner.requestBudget.ReservedBytes {
		return planner.failure(page.Path, "", RetirementRequestLimitCode, ErrRetirementRequestLimit)
	}
	model, err := planner.maintainer.PlanRetirement(ctx, input)
	if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
		return knowl.RetirementPlanResult{}, ctxErr
	}
	if err != nil {
		return planner.failure(page.Path, "", RetirementMaintainerFailedCode, errors.Join(ErrRetirementMaintainerFailed, err))
	}
	result, err := planner.validateModel(page, request.Schema.Digest, evidence, retiringRefs, model)
	if ctxErr := contextErr(ctx); ctxErr != nil {
		return knowl.RetirementPlanResult{}, ctxErr
	}
	if err != nil {
		return result, err
	}
	if err := planner.revalidateAuthority(ctx, request); err != nil {
		if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
			return knowl.RetirementPlanResult{}, ctxErr
		}
		return planner.failure(page.Path, "", RetirementLifecycleConflictCode, err)
	}
	return result, nil
}

func (planner *RetirementPlanner) validateRequest(request RetirementPlanningRequest) error {
	if strings.TrimSpace(string(request.Scope)) == "" || request.Page.ID == "" || request.Page.Path == "" || request.Page.Digest == "" ||
		request.Authority.Scope != request.Scope || request.Authority.Generation == "" || request.Authority.Epoch == 0 ||
		request.Schema.Scope != request.Scope || !validExecutionSchema(request.Scope, request.Schema) || len(request.Retiring) == 0 {
		return ErrRetirementPlanInvalid
	}
	canonical, err := validateEditPath(request.Page.Path)
	pageID, ordinary := knowlwiki.PageIDFromPath(request.Page.Path)
	if err != nil || canonical != request.Page.Path || !ordinary || pageID != request.Page.ID || !validExecutionDigest(request.Page.Digest) {
		return ErrRetirementPlanInvalid
	}
	if _, err := validateRetirementCatalogs(request.Catalogs, planner.catalogLimits); err != nil {
		return err
	}
	return nil
}

func (planner *RetirementPlanner) validateModel(page knowl.PageSnapshot, schemaDigest string, evidence []knowl.RetirementEvidence, retiringRefs map[string]struct{}, model knowl.RetirementModelPlan) (knowl.RetirementPlanResult, error) {
	if model.SchemaDigest != schemaDigest || len(model.Rationale) > planner.planLimits.MaxRationaleSize {
		return planner.failure(page.Path, "", RetirementPlanInvalidCode, ErrRetirementPlanInvalid)
	}
	if len(model.SourceRefs) > min(maxRetirementEvidenceRefs, planner.planLimits.MaxSourceRefs) || len(model.Pages) > 1 || (model.Delete && (len(model.Pages) != 0 || len(model.SourceRefs) != 0)) {
		code, cause := RetirementPlanInvalidCode, ErrRetirementPlanInvalid
		if len(model.SourceRefs) > min(maxRetirementEvidenceRefs, planner.planLimits.MaxSourceRefs) {
			code, cause = RetirementOutputLimitCode, ErrRetirementOutputLimit
		}
		return planner.failure(page.Path, "", code, cause)
	}
	if model.Delete {
		return retirementDeleteResult(page), nil
	}
	if len(model.Pages) != 1 {
		return planner.failure(page.Path, "", RetirementPlanInvalidCode, ErrRetirementPlanInvalid)
	}
	edit := model.Pages[0]
	if len(edit.Content) > planner.planLimits.MaxFileBytes {
		return planner.failure(page.Path, "", RetirementOutputLimitCode, ErrRetirementOutputLimit)
	}
	if edit.Path != page.Path || edit.ExpectedDigest != page.Digest || len(edit.Content) == 0 {
		return planner.failure(page.Path, "", RetirementPlanInvalidCode, ErrRetirementPlanInvalid)
	}
	metadata, err := knowlwiki.ParseFrontmatter(string(edit.Content))
	if err != nil || metadata.ID != string(page.ID) || metadata.Type == "" {
		return planner.failure(page.Path, "", RetirementPageMalformedCode, ErrRetirementPlanInvalid)
	}
	contentRefs, duplicateContentRef := normalizedUniqueRefs(metadata.SourceRefs)
	if duplicateContentRef || len(contentRefs) == 0 {
		return planner.failure(page.Path, "", RetirementPageMalformedCode, ErrRetirementPlanInvalid)
	}
	if navigation := knowlwiki.InspectNavigation(strings.TrimPrefix(page.Path, "wiki/"), knowlwiki.Body(string(edit.Content)), 4096); navigation.Rule != "" {
		return planner.failure(page.Path, "", RetirementPageMalformedCode, ErrRetirementPlanInvalid)
	}
	declared, duplicate := normalizedUniqueRefs(model.SourceRefs)
	if duplicate || len(declared) == 0 {
		used := knowlwiki.SourceRefs(string(edit.Content))
		if len(used) > 0 && len(declared) == 0 {
			return planner.failure(page.Path, used[0], CitationPlanMissingCode, ErrRetirementPlanInvalid)
		}
		return planner.failure(page.Path, "", RetirementPlanInvalidCode, ErrRetirementPlanInvalid)
	}
	used := contentRefs
	for _, ref := range used {
		if !slices.Contains(declared, ref) {
			return planner.failure(page.Path, ref, CitationPlanMissingCode, ErrRetirementPlanInvalid)
		}
	}
	if !slices.Equal(used, declared) {
		return planner.failure(page.Path, "", RetirementPlanInvalidCode, ErrRetirementPlanInvalid)
	}
	allowed := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		allowed[item.SourceRef] = struct{}{}
	}
	for _, ref := range declared {
		if _, ok := allowed[ref]; ok {
			continue
		}
		if _, retiring := retiringRefs[ref]; retiring {
			return planner.failure(page.Path, ref, CitationSourceRetiringCode, ErrRetirementPlanInvalid)
		}
		return planner.failure(page.Path, ref, knowl.DiagnosticCitationUnknownSource, ErrRetirementPlanInvalid)
	}
	resultPage := clonePage(page)
	resultPage.Content = string(slices.Clone(edit.Content))
	resultPage.Body = knowlwiki.Body(resultPage.Content)
	resultPage.Title = metadata.Title
	resultPage.SourceRefs = slices.Clone(declared)
	return knowl.RetirementPlanResult{Action: knowl.RetirementReplace, Page: resultPage, SourceRefs: slices.Clone(declared), Diagnostics: []knowl.MaintenanceDiagnostic{}}, nil
}

func (planner *RetirementPlanner) retirementRequestBytes(ctx context.Context, input knowl.RetirementInput) (int, error) {
	if planner.requestSizer != nil {
		return planner.requestSizer.RetirementRequestBytes(ctx, input)
	}
	encoded, err := EncodeRetirementMaintenanceRequest(ctx, input)
	return len(encoded), err
}

// EncodeRetirementMaintenanceRequest returns the canonical provider envelope.
func EncodeRetirementMaintenanceRequest(ctx context.Context, input knowl.RetirementInput) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if input.ContractVersion != RetirementMaintenanceContractVersion || strings.TrimSpace(string(input.Scope)) == "" || input.Schema.Scope != input.Scope || len(input.Evidence) == 0 {
		return nil, ErrRetirementPlanInvalid
	}
	envelope := struct {
		Operation            string                `json:"operation"`
		Input                knowl.RetirementInput `json:"input"`
		RequiredSchemaDigest string                `json:"required_schema_digest"`
		AllowedSourceRefs    []string              `json:"allowed_source_refs"`
	}{Operation: "source_retirement", Input: input, RequiredSchemaDigest: input.Schema.Digest, AllowedSourceRefs: make([]string, len(input.Evidence))}
	for index, evidence := range input.Evidence {
		envelope.AllowedSourceRefs[index] = evidence.SourceRef
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, ErrRetirementPlanInvalid
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	return encoded, nil
}

func (planner *RetirementPlanner) revalidateAuthority(ctx context.Context, request RetirementPlanningRequest) error {
	authority, err := planner.lifecycles.SourceLifecycleAuthority(ctx, request.Scope)
	if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
		return ctxErr
	}
	if err != nil || !sameRetirementAuthority(authority, request.Authority) {
		return errors.Join(ErrRetirementLifecycleConflict, err)
	}
	return nil
}

func retirementContextError(ctx context.Context, err error) error {
	if ctxErr := contextErr(ctx); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func (planner *RetirementPlanner) failure(path, target, code string, cause error) (knowl.RetirementPlanResult, error) {
	if !validDiagnosticIdentifier(target, true) {
		target = ""
	}
	diagnostics, err := NormalizeMaintenanceDiagnostics([]knowl.MaintenanceDiagnostic{{Code: code, Path: path, Target: target}})
	if err != nil {
		return knowl.RetirementPlanResult{}, errors.Join(ErrRetirementPlanInvalid, err)
	}
	return knowl.RetirementPlanResult{Diagnostics: diagnostics}, cause
}

type retirementRefFailure struct{ ref string }

func (failure *retirementRefFailure) Error() string { return RetirementEvidenceInvalidCode }

func normalizeRequestedRetirementRefs(input, pageRefs []string) ([]string, error) {
	requested := slices.Clone(input)
	sort.Strings(requested)
	duplicate := false
	for index, ref := range requested {
		if ref == "" || strings.TrimSpace(ref) != ref || index > 0 && requested[index-1] == ref {
			duplicate = true
			break
		}
	}
	wanted, invalidPage := normalizedUniqueRefs(pageRefs)
	if duplicate || invalidPage {
		target := ""
		if len(requested) > 0 {
			target = requested[0]
		}
		return nil, &retirementRefFailure{ref: target}
	}
	requestedSet := make(map[string]struct{}, len(requested))
	wantedSet := make(map[string]struct{}, len(wanted))
	for _, ref := range requested {
		requestedSet[ref] = struct{}{}
	}
	for _, ref := range wanted {
		wantedSet[ref] = struct{}{}
	}
	differences := make([]string, 0, max(len(requested), len(wanted)))
	for _, ref := range requested {
		if _, ok := wantedSet[ref]; !ok {
			differences = append(differences, ref)
		}
	}
	for _, ref := range wanted {
		if _, ok := requestedSet[ref]; !ok {
			differences = append(differences, ref)
		}
	}
	if len(differences) > 0 {
		sort.Strings(differences)
		return nil, &retirementRefFailure{ref: differences[0]}
	}
	return requested, nil
}

func validateResolvedRetirementSource(scope knowl.ScopeRef, ref string, source knowl.AcceptedSource) error {
	owner := sourceOwner(source)
	if source.Scope != scope || !SourceRefMatches(ref, source) || ValidateAcceptedSourceDescriptor(source) != nil ||
		!strings.HasPrefix(source.ManifestRef, "raw/") || !strings.HasSuffix(source.ManifestRef, "/manifest.yaml") || ValidateSourceOwner(owner) != nil {
		return ErrRetirementPlanInvalid
	}
	return nil
}

func normalizeAuthorizedRetiring(scope knowl.ScopeRef, input []knowl.SourceLifecycle, authority knowl.SourceLifecycleAuthority) (map[knowl.SourceOwner]knowl.SourceLifecycle, error) {
	if len(input) == 0 || len(input) > maxDesiredSourceOwners {
		return nil, ErrRetirementLifecycleConflict
	}
	result := make(map[knowl.SourceOwner]knowl.SourceLifecycle, len(input))
	for _, state := range input {
		if state.Scope != scope || state.State != knowl.SourceLifecycleRetiring || state.Owner == (knowl.SourceOwner{}) ||
			state.AuthorityGeneration != authority.Generation || state.AuthorityEpoch != authority.Epoch ||
			state.RetirementGeneration == "" || state.RetirementEpoch == 0 {
			return nil, ErrRetirementLifecycleConflict
		}
		if _, duplicate := result[state.Owner]; duplicate {
			return nil, ErrRetirementLifecycleConflict
		}
		result[state.Owner] = state
	}
	return result, nil
}

func sameRetiringLifecycle(actual, expected knowl.SourceLifecycle) bool {
	return actual.Scope == expected.Scope && actual.Owner == expected.Owner && actual.State == knowl.SourceLifecycleRetiring &&
		actual.AuthorityGeneration == expected.AuthorityGeneration && actual.AuthorityEpoch == expected.AuthorityEpoch &&
		actual.ConfigDigest == expected.ConfigDigest && actual.RetirementGeneration == expected.RetirementGeneration && actual.RetirementEpoch == expected.RetirementEpoch &&
		actual.Ready == expected.Ready && actual.ReplacementPending == expected.ReplacementPending && actual.BlockerCode == expected.BlockerCode
}

func sameRetirementAuthority(left, right knowl.SourceLifecycleAuthority) bool {
	return left.Scope == right.Scope && left.Generation == right.Generation && left.Epoch == right.Epoch
}

func sameCurrentRetirementPage(current knowl.PageSnapshot, expected CanonicalPageProvenance) bool {
	if current.ID != expected.ID || current.Path != expected.Path || current.Digest != expected.Digest {
		return false
	}
	left, duplicateLeft := normalizedUniqueRefs(current.SourceRefs)
	right, duplicateRight := normalizedUniqueRefs(expected.SourceRefs)
	return !duplicateLeft && !duplicateRight && slices.Equal(left, right)
}

func normalizeRetirementPage(page knowl.PageSnapshot) knowl.PageSnapshot {
	page = clonePage(page)
	page.SourceRefs, _ = normalizedUniqueRefs(page.SourceRefs)
	return page
}

func normalizedUniqueRefs(refs []string) ([]string, bool) {
	result := slices.Clone(refs)
	for index := range result {
		trimmed := strings.TrimSpace(result[index])
		if trimmed != result[index] {
			return result, true
		}
		result[index] = trimmed
	}
	sort.Strings(result)
	duplicate := false
	for index, ref := range result {
		if ref == "" || index > 0 && result[index-1] == ref {
			duplicate = true
		}
	}
	return result, duplicate
}

func supportedRetirementEvidence(mediaType string, content []byte) bool {
	return supportedRetirementMediaType(mediaType) && utf8.Valid(content)
}

func supportedRetirementMediaType(mediaType string) bool {
	parsed, _, err := mime.ParseMediaType(mediaType)
	return err == nil && (parsed == "text/plain" || parsed == "text/markdown")
}

func sourceOwner(source knowl.AcceptedSource) knowl.SourceOwner {
	return knowl.SourceOwner{Adapter: knowl.SourceAdapterID(source.Source.Adapter), SourceID: source.SourceDocument.SourceID}
}

func retirementDeleteResult(page knowl.PageSnapshot) knowl.RetirementPlanResult {
	page = clonePage(page)
	page.Content, page.Body, page.SourceRefs, page.SourceDocuments = "", "", []string{}, []knowl.SourceDocument{}
	page.SourceDocument = nil
	return knowl.RetirementPlanResult{Action: knowl.RetirementDelete, Page: page, SourceRefs: []string{}, Diagnostics: []knowl.MaintenanceDiagnostic{}}
}

func normalizeRetirementCatalogs(input []knowl.HierarchyCatalog) []knowl.HierarchyCatalog {
	result := slices.Clone(input)
	for index := range result {
		result[index].Children = slices.Clone(result[index].Children)
		sort.Strings(result[index].Children)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result
}

func validateRetirementCatalogs(input []knowl.HierarchyCatalog, limits knowl.CatalogLimits) ([]knowl.HierarchyCatalog, error) {
	if len(input) == 0 || len(input) > limits.MaxCatalogs {
		return nil, ErrRetirementPlanInvalid
	}
	result := normalizeRetirementCatalogs(input)
	seen := make(map[string]struct{}, len(result))
	edges := 0
	for _, catalog := range result {
		canonical, kind, err := validateHierarchyWikiPath(catalog.Path, limits.MaxPathBytes)
		if err != nil || canonical != catalog.Path || kind != okf.DocumentIndex || !validExecutionDigest(catalog.Digest) ||
			strings.TrimSpace(catalog.Title) == "" || !utf8.ValidString(catalog.Title) {
			return nil, ErrRetirementPlanInvalid
		}
		if _, duplicate := seen[catalog.Path]; duplicate {
			return nil, ErrRetirementPlanInvalid
		}
		seen[catalog.Path] = struct{}{}
		edges += len(catalog.Children)
		if edges > limits.MaxEdges {
			return nil, ErrRetirementPlanInvalid
		}
		for _, child := range catalog.Children {
			canonicalChild, _, childErr := validateHierarchyWikiPath(child, limits.MaxPathBytes)
			if childErr != nil || canonicalChild != child {
				return nil, ErrRetirementPlanInvalid
			}
		}
	}
	if _, root := seen[rootCatalogPath]; !root {
		return nil, ErrRetirementPlanInvalid
	}
	if err := validateCatalogDepth(result, limits.MaxDepth); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > limits.MaxInputBytes {
		return nil, ErrRetirementPlanInvalid
	}
	return result, nil
}

func cloneRetirementEvidence(input []knowl.RetirementEvidence) []knowl.RetirementEvidence {
	result := slices.Clone(input)
	return result
}

func clonePage(page knowl.PageSnapshot) knowl.PageSnapshot {
	page.SourceRefs = slices.Clone(page.SourceRefs)
	page.SourceDocuments = slices.Clone(page.SourceDocuments)
	if page.OKF != nil {
		metadata := *page.OKF
		page.OKF = &metadata
	}
	if page.SourceDocument != nil {
		document := *page.SourceDocument
		page.SourceDocument = &document
	}
	return page
}

func cloneSchema(schema knowl.SchemaDocument) knowl.SchemaDocument {
	schema.Content = slices.Clone(schema.Content)
	return schema
}
