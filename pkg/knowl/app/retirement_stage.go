package app

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
)

const (
	// RetirementCatalogClosureLimitCode identifies a page unit whose smallest
	// required navigation closure does not fit the existing plan bounds.
	RetirementCatalogClosureLimitCode = "retirement.catalog_closure_limit"
	// RetirementCatalogMixedItemCode identifies a catalog item that combines
	// a retiring target with semantics that cannot be removed independently.
	RetirementCatalogMixedItemCode = "retirement.catalog_mixed_item"
)

var (
	ErrRetirementCatalogClosureLimit = errors.New(RetirementCatalogClosureLimitCode)
	ErrRetirementCatalogMixedItem    = errors.New(RetirementCatalogMixedItemCode)
)

// RetirementUnitError preserves bounded path-bearing diagnostics for a page
// unit that cannot be constructed safely.
type RetirementUnitError struct {
	Code        string
	Diagnostics []knowl.MaintenanceDiagnostic
	Cause       error
}

func (failure *RetirementUnitError) Error() string { return failure.Code }

func (failure *RetirementUnitError) Unwrap() error { return failure.Cause }

// RetirementCommitter is one structurally bound capability that owns the
// complete fenced retirement apply session. The callbacks cannot publish
// canonical content and run only before and after the sealed canonical step.
type RetirementCommitter interface {
	RunRetirementApply(
		ctx context.Context,
		staged knowl.StagedChange,
		prepare func(context.Context) (bool, error),
		complete func(context.Context, knowl.ContentCommit) error,
	) (knowl.ContentCommit, error)
}

// ValidateRetirementPlanContext validates the bounded exact page and owner
// authority encoded in a plan or authenticated stage.
func ValidateRetirementPlanContext(retirement *knowl.RetirementPlanContext) error {
	if retirement == nil || strings.TrimSpace(string(retirement.Scope)) == "" || retirement.PageID == "" ||
		retirement.PagePath != "wiki/"+string(retirement.PageID)+".md" || !validExecutionDigest(retirement.PageDigest) ||
		!validExecutionDigest(retirement.AuthorityGeneration) || retirement.AuthorityEpoch == 0 ||
		len(retirement.Owners) == 0 || len(retirement.Owners) > maxDesiredSourceOwners {
		return ErrRetirementLifecycleConflict
	}
	if canonical, err := validateEditPath(retirement.PagePath); err != nil || canonical != retirement.PagePath {
		return ErrRetirementLifecycleConflict
	}
	var previous knowl.SourceOwner
	for index, authorized := range retirement.Owners {
		if ValidateSourceOwner(authorized.Owner) != nil || !validExecutionDigest(authorized.ConfigDigest) || !validExecutionDigest(authorized.RetirementGeneration) ||
			authorized.RetirementEpoch == 0 || authorized.RetirementEpoch > retirement.AuthorityEpoch {
			return ErrRetirementLifecycleConflict
		}
		if index > 0 && !sourceOwnerLess(previous, authorized.Owner) {
			return ErrRetirementLifecycleConflict
		}
		previous = authorized.Owner
	}
	return nil
}

// RevalidateRetirementStage checks one stage against the lifecycle state read
// while a bound RetirementCommitter holds its shared apply fence.
func RevalidateRetirementStage(ctx context.Context, lifecycles SourceLifecycleReader, staged knowl.StagedChange) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if lifecycles == nil || strings.TrimSpace(staged.OperationID) == "" || !validExecutionDigest(staged.Digest) || ValidateRetirementPlanContext(staged.Retirement) != nil {
		return ErrRetirementLifecycleConflict
	}
	retirement := staged.Retirement
	authority, err := lifecycles.SourceLifecycleAuthority(ctx, retirement.Scope)
	if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
		return ctxErr
	}
	if err != nil || authority.Scope != retirement.Scope || authority.Generation != retirement.AuthorityGeneration || authority.Epoch != retirement.AuthorityEpoch {
		return errors.Join(ErrRetirementLifecycleConflict, err)
	}
	for _, authorized := range retirement.Owners {
		state, stateErr := lifecycles.SourceLifecycle(ctx, retirement.Scope, authorized.Owner)
		if ctxErr := retirementContextError(ctx, stateErr); ctxErr != nil {
			return ctxErr
		}
		if stateErr != nil || ValidateSourceLifecycle(state) != nil || state.Scope != retirement.Scope || state.Owner != authorized.Owner ||
			state.State != knowl.SourceLifecycleRetiring || state.AuthorityGeneration != retirement.AuthorityGeneration || state.AuthorityEpoch != retirement.AuthorityEpoch ||
			state.ConfigDigest != authorized.ConfigDigest || state.RetirementGeneration != authorized.RetirementGeneration || state.RetirementEpoch != authorized.RetirementEpoch ||
			state.ReplacementPending != 0 || state.BlockerCode != "" {
			return errors.Join(ErrRetirementLifecycleConflict, stateErr)
		}
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	return nil
}

// BuildRetirementEditPlan converts one already application-validated
// recomputation result into one page-scoped staged-plan unit. A delete includes
// only catalogs that directly reference the removed page.
func (planner *RetirementPlanner) BuildRetirementEditPlan(ctx context.Context, operationID string, request RetirementPlanningRequest, result knowl.RetirementPlanResult) (knowl.ValidatedEditPlan, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.ValidatedEditPlan{}, err
	}
	if planner == nil || planner.content == nil || planner.lifecycles == nil || strings.TrimSpace(operationID) == "" {
		return knowl.ValidatedEditPlan{}, ErrRetirementPlanInvalid
	}
	if err := planner.revalidateRetirementBuild(ctx, request, result); err != nil {
		return knowl.ValidatedEditPlan{}, err
	}
	limits := planner.planLimits
	if request.Scope == "" || request.Schema.Scope != request.Scope || !validExecutionSchema(request.Scope, request.Schema) ||
		request.Page.ID == "" || request.Page.Path == "" || !validExecutionDigest(request.Page.Digest) ||
		result.Page.ID != request.Page.ID || result.Page.Path != request.Page.Path || result.Page.Digest != request.Page.Digest || len(result.Diagnostics) != 0 {
		return knowl.ValidatedEditPlan{}, ErrRetirementPlanInvalid
	}
	canonical, err := validateEditPath(request.Page.Path)
	if err != nil || canonical != request.Page.Path || canonical != "wiki/"+string(request.Page.ID)+".md" {
		return knowl.ValidatedEditPlan{}, ErrRetirementPlanInvalid
	}
	retirement, err := retirementPlanContext(request)
	if err != nil {
		return knowl.ValidatedEditPlan{}, err
	}
	edits := []knowl.FileEdit{{Path: request.Page.Path, ExpectedDigest: request.Page.Digest}}
	sourceRefs := slices.Clone(result.SourceRefs)
	sort.Strings(sourceRefs)
	switch result.Action {
	case knowl.RetirementReplace:
		if len(sourceRefs) == 0 || len(sourceRefs) > limits.MaxSourceRefs || len(result.Page.Content) == 0 || len(result.Page.Content) > limits.MaxFileBytes || !slices.Equal(sourceRefs, result.Page.SourceRefs) {
			return knowl.ValidatedEditPlan{}, ErrRetirementPlanInvalid
		}
		edits[0].Action = knowl.SourceMutationWrite
		edits[0].Content = []byte(result.Page.Content)
	case knowl.RetirementDelete:
		if len(sourceRefs) != 0 || result.Page.Content != "" || result.Page.Body != "" || len(result.Page.SourceRefs) != 0 {
			return knowl.ValidatedEditPlan{}, ErrRetirementPlanInvalid
		}
		edits[0].Action = knowl.SourceMutationDelete
		catalogs, validateErr := validateRetirementCatalogs(request.Catalogs, planner.catalogLimits)
		if validateErr != nil {
			return knowl.ValidatedEditPlan{}, validateErr
		}
		closure, closureErr := planner.retirementCatalogClosure(ctx, request.Scope, request.Page.Path, catalogs, limits.MaxFileBytes)
		if closureErr != nil {
			return knowl.ValidatedEditPlan{}, closureErr
		}
		if len(edits)+len(closure) > limits.MaxFiles {
			target := ""
			if len(closure) > 0 {
				target = closure[0].Path
			}
			return knowl.ValidatedEditPlan{}, retirementUnitFailure(request.Page.Path, target, RetirementCatalogClosureLimitCode, ErrRetirementCatalogClosureLimit)
		}
		edits = append(edits, closure...)
	default:
		return knowl.ValidatedEditPlan{}, ErrRetirementPlanInvalid
	}
	if len(edits) > limits.MaxFiles {
		return knowl.ValidatedEditPlan{}, retirementUnitFailure(request.Page.Path, "", RetirementCatalogClosureLimitCode, ErrRetirementCatalogClosureLimit)
	}
	sort.Slice(edits, func(left, right int) bool { return edits[left].Path < edits[right].Path })
	return knowl.ValidatedEditPlan{
		OperationID: operationID, Scope: request.Scope, SchemaDigest: request.Schema.Digest,
		SourceRefs: sourceRefs, Edits: edits, Diagnostics: []knowl.MaintenanceDiagnostic{}, Retirement: retirement,
	}, nil
}

func (planner *RetirementPlanner) revalidateRetirementBuild(ctx context.Context, request RetirementPlanningRequest, result knowl.RetirementPlanResult) error {
	if err := planner.validateRequest(request); err != nil {
		return ErrRetirementPlanInvalid
	}
	authority, err := planner.lifecycles.SourceLifecycleAuthority(ctx, request.Scope)
	if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
		return ctxErr
	}
	if err != nil || !sameRetirementAuthority(authority, request.Authority) {
		return errors.Join(ErrRetirementLifecycleConflict, err)
	}
	pages, err := planner.content.ReadPages(ctx, request.Scope, []knowl.PageID{request.Page.ID}, planner.readLimits)
	if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
		return ctxErr
	}
	if err != nil || len(pages) != 1 || !sameCurrentRetirementPage(pages[0], request.Page) || digestHierarchyBytes([]byte(pages[0].Content)) != pages[0].Digest {
		return errors.Join(ErrRetirementPageStale, err)
	}
	if result.Page.ID != request.Page.ID || result.Page.Path != request.Page.Path || result.Page.Digest != request.Page.Digest {
		return ErrRetirementPageStale
	}
	retiring, err := normalizeAuthorizedRetiring(request.Scope, request.Retiring, request.Authority)
	if err != nil {
		return errors.Join(ErrRetirementLifecycleConflict, err)
	}
	for owner, expected := range retiring {
		current, readErr := planner.lifecycles.SourceLifecycle(ctx, request.Scope, owner)
		if ctxErr := retirementContextError(ctx, readErr); ctxErr != nil {
			return ctxErr
		}
		if readErr != nil || !sameRetiringLifecycle(current, expected) || current.ReplacementPending != 0 || current.BlockerCode != "" {
			return errors.Join(ErrRetirementLifecycleConflict, readErr)
		}
	}
	return contextErr(ctx)
}

func retirementPlanContext(request RetirementPlanningRequest) (*knowl.RetirementPlanContext, error) {
	if request.Authority.Scope != request.Scope || !validExecutionDigest(request.Authority.Generation) || request.Authority.Epoch == 0 || len(request.Retiring) == 0 || len(request.Retiring) > maxDesiredSourceOwners {
		return nil, ErrRetirementLifecycleConflict
	}
	owners := make([]knowl.RetirementOwnerAuthority, 0, len(request.Retiring))
	seen := make(map[knowl.SourceOwner]struct{}, len(request.Retiring))
	for _, state := range request.Retiring {
		if ValidateSourceLifecycle(state) != nil || state.Scope != request.Scope || state.State != knowl.SourceLifecycleRetiring ||
			state.AuthorityGeneration != request.Authority.Generation || state.AuthorityEpoch != request.Authority.Epoch || state.ReplacementPending != 0 || state.BlockerCode != "" {
			return nil, ErrRetirementLifecycleConflict
		}
		if _, duplicate := seen[state.Owner]; duplicate {
			return nil, ErrRetirementLifecycleConflict
		}
		seen[state.Owner] = struct{}{}
		owners = append(owners, knowl.RetirementOwnerAuthority{Owner: state.Owner, ConfigDigest: state.ConfigDigest, RetirementGeneration: state.RetirementGeneration, RetirementEpoch: state.RetirementEpoch})
	}
	sort.Slice(owners, func(left, right int) bool {
		if owners[left].Owner.Adapter != owners[right].Owner.Adapter {
			return owners[left].Owner.Adapter < owners[right].Owner.Adapter
		}
		return owners[left].Owner.SourceID < owners[right].Owner.SourceID
	})
	return &knowl.RetirementPlanContext{
		Scope: request.Scope, PageID: request.Page.ID, PagePath: request.Page.Path, PageDigest: request.Page.Digest,
		AuthorityGeneration: request.Authority.Generation, AuthorityEpoch: request.Authority.Epoch, Owners: owners,
	}, nil
}

func sourceOwnerLess(left, right knowl.SourceOwner) bool {
	if left.Adapter != right.Adapter {
		return left.Adapter < right.Adapter
	}
	return left.SourceID < right.SourceID
}

func (planner *RetirementPlanner) retirementCatalogClosure(ctx context.Context, scope knowl.ScopeRef, pagePath string, catalogs []knowl.HierarchyCatalog, maxBytes int) ([]knowl.FileEdit, error) {
	affected := make([]knowl.HierarchyCatalog, 0)
	for _, catalog := range catalogs {
		if slices.Contains(catalog.Children, pagePath) {
			affected = append(affected, catalog)
		}
	}
	if len(affected) == 0 {
		return nil, ErrRetirementPlanInvalid
	}
	ids := make([]knowl.PageID, len(affected))
	for index, catalog := range affected {
		ids[index] = knowl.PageID(strings.TrimSuffix(strings.TrimPrefix(catalog.Path, "wiki/"), ".md"))
	}
	snapshots, err := planner.content.ReadPages(ctx, scope, ids, planner.readLimits)
	if ctxErr := retirementContextError(ctx, err); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil || len(snapshots) != len(affected) {
		return nil, errors.Join(ErrRetirementPlanInvalid, err)
	}
	byPath := make(map[string]knowl.PageSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byPath[snapshot.Path] = snapshot
	}
	edits := make([]knowl.FileEdit, 0)
	readBytes := 0
	readEdges := 0
	for _, catalog := range affected {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		snapshot, found := byPath[catalog.Path]
		if !found || snapshot.Digest != catalog.Digest || digestHierarchyBytes([]byte(snapshot.Content)) != snapshot.Digest {
			return nil, ErrRetirementPlanInvalid
		}
		if _, shapeErr := retirementCatalogRemovalRanges(catalog.Path, []byte(snapshot.Content), pagePath); shapeErr != nil {
			return nil, shapeErr
		}
		fresh, bytes, edges, graphErr := freshRetirementCatalog(snapshot, planner.catalogLimits, readBytes, readEdges)
		if errors.Is(graphErr, ErrPlanLimitExceeded) {
			return nil, retirementUnitFailure(pagePath, catalog.Path, RetirementCatalogClosureLimitCode, errors.Join(ErrRetirementCatalogClosureLimit, graphErr))
		}
		if graphErr != nil || !sameRetirementCatalog(fresh, catalog) {
			return nil, errors.Join(ErrRetirementPlanInvalid, graphErr)
		}
		readBytes, readEdges = bytes, edges
		content, changed, renderErr := removeRetiringCatalogLinks(catalog.Path, []byte(snapshot.Content), pagePath, maxBytes)
		if renderErr != nil {
			if errors.Is(renderErr, ErrRetirementCatalogMixedItem) {
				return nil, renderErr
			}
			return nil, retirementUnitFailure(pagePath, catalog.Path, RetirementCatalogClosureLimitCode, errors.Join(ErrRetirementCatalogClosureLimit, renderErr))
		}
		if !changed {
			continue
		}
		edits = append(edits, knowl.FileEdit{Action: knowl.SourceMutationWrite, Path: catalog.Path, ExpectedDigest: catalog.Digest, Content: content})
	}
	sort.Slice(edits, func(left, right int) bool { return hierarchyCatalogPathLess(edits[left].Path, edits[right].Path) })
	return edits, nil
}

func freshRetirementCatalog(snapshot knowl.PageSnapshot, limits knowl.CatalogLimits, priorBytes, priorEdges int) (knowl.HierarchyCatalog, int, int, error) {
	canonical, kind, err := validateHierarchyWikiPath(snapshot.Path, limits.MaxPathBytes)
	readBytes := priorBytes + len(snapshot.Content)
	if len(snapshot.Content) > limits.MaxCatalogBytes || readBytes > limits.MaxSnapshotBytes {
		return knowl.HierarchyCatalog{}, priorBytes, priorEdges, ErrPlanLimitExceeded
	}
	if err != nil || canonical != snapshot.Path || kind != okf.DocumentIndex || !validExecutionDigest(snapshot.Digest) ||
		strings.TrimSpace(snapshot.Title) == "" || !utf8.ValidString(snapshot.Title) || !utf8.ValidString(snapshot.Content) {
		return knowl.HierarchyCatalog{}, priorBytes, priorEdges, ErrRetirementPlanInvalid
	}
	destinations, malformed := wiki.IndexDestinations(snapshot.Content, len(snapshot.Content)+1)
	if malformed {
		return knowl.HierarchyCatalog{}, priorBytes, priorEdges, ErrRetirementPlanInvalid
	}
	children := make(map[string]struct{}, len(destinations))
	for _, destination := range destinations {
		target, external, valid := wiki.ResolveIndexDestination(strings.TrimPrefix(snapshot.Path, "wiki/"), destination)
		if !valid {
			return knowl.HierarchyCatalog{}, priorBytes, priorEdges, ErrRetirementPlanInvalid
		}
		if !external {
			children["wiki/"+target] = struct{}{}
		}
	}
	readEdges := priorEdges + len(children)
	if readEdges > limits.MaxEdges {
		return knowl.HierarchyCatalog{}, priorBytes, priorEdges, ErrPlanLimitExceeded
	}
	fresh := knowl.HierarchyCatalog{Path: snapshot.Path, Digest: snapshot.Digest, Title: snapshot.Title, Children: make([]string, 0, len(children))}
	for child := range children {
		fresh.Children = append(fresh.Children, child)
	}
	sort.Strings(fresh.Children)
	return fresh, readBytes, readEdges, nil
}

func sameRetirementCatalog(left, right knowl.HierarchyCatalog) bool {
	children := slices.Clone(right.Children)
	sort.Strings(children)
	return left.Path == right.Path && left.Digest == right.Digest && left.Title == right.Title && slices.Equal(left.Children, children)
}

type retirementByteRange struct{ start, end int }

func removeRetiringCatalogLinks(catalogPath string, content []byte, pagePath string, maxBytes int) ([]byte, bool, error) {
	relative := strings.TrimPrefix(catalogPath, "wiki/")
	limits := okf.DefaultLimits()
	limits.MaxBytes = maxBytes
	index, err := okf.ValidateIndex(relative, content, limits)
	if err != nil {
		return nil, false, err
	}
	body := []byte(index.Body)
	ranges, err := retirementCatalogRemovalRanges(catalogPath, body, pagePath)
	if err != nil {
		return content, false, err
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start > ranges[j].start })
	for _, remove := range ranges {
		body = slices.Delete(body, remove.start, remove.end)
	}
	index.Body = string(body)
	rendered, err := okf.RenderIndex(relative, index, limits)
	if err != nil {
		return nil, false, err
	}
	retainsTarget, graphErr := retirementCatalogReferencesPage(relative, rendered, pagePath)
	if graphErr != nil || retainsTarget {
		return content, false, retirementUnitFailure(catalogPath, pagePath, RetirementCatalogMixedItemCode, errors.Join(ErrRetirementCatalogMixedItem, graphErr))
	}
	return rendered, true, nil
}

type retirementCatalogItemAction uint8

const (
	retirementCatalogItemUnrelated retirementCatalogItemAction = iota
	retirementCatalogItemExact
	retirementCatalogItemMixed
)

func classifyRetirementCatalogItem(item *ast.ListItem, source []byte, relative, pagePath string) retirementCatalogItemAction {
	if !retirementItemContainsTarget(item, source, relative, pagePath) {
		return retirementCatalogItemUnrelated
	}
	if retirementCatalogItemIsExactLink(item, source, relative, pagePath) {
		return retirementCatalogItemExact
	}
	return retirementCatalogItemMixed
}

func retirementCatalogRemovalRanges(catalogPath string, source []byte, pagePath string) ([]retirementByteRange, error) {
	relative := strings.TrimPrefix(catalogPath, "wiki/")
	document := parser.New().Parse(source)
	ranges := make([]retirementByteRange, 0)
	err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		item, ok := node.(*ast.ListItem)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		switch classifyRetirementCatalogItem(item, source, relative, pagePath) {
		case retirementCatalogItemMixed:
			return ast.WalkStop, retirementUnitFailure(catalogPath, pagePath, RetirementCatalogMixedItemCode, ErrRetirementCatalogMixedItem)
		case retirementCatalogItemExact:
			start, end, valid := retirementListItemRange(item, source)
			if !valid {
				return ast.WalkStop, ErrRetirementPlanInvalid
			}
			ranges = append(ranges, retirementByteRange{start: start, end: end})
			return ast.WalkSkipChildren, nil
		default:
			return ast.WalkContinue, nil
		}
	})
	if err != nil {
		return nil, err
	}
	if len(ranges) == 0 {
		return nil, retirementUnitFailure(catalogPath, pagePath, RetirementCatalogMixedItemCode, ErrRetirementCatalogMixedItem)
	}
	return ranges, nil
}

func retirementItemContainsTarget(item *ast.ListItem, source []byte, relative, pagePath string) bool {
	contains := false
	for child := item.FirstChild(); child != nil && !contains; child = child.NextSibling() {
		_ = ast.Walk(child, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			if _, nested := node.(*ast.List); nested {
				return ast.WalkSkipChildren, nil
			}
			var destination string
			switch typed := node.(type) {
			case *ast.Link:
				destination = typed.Destination.Value(source)
			case *ast.Image:
				destination = typed.Destination.Value(source)
			default:
				return ast.WalkContinue, nil
			}
			target, external, valid := wiki.ResolveIndexDestination(relative, destination)
			if valid && !external && "wiki/"+target == pagePath {
				contains = true
				return ast.WalkStop, nil
			}
			return ast.WalkContinue, nil
		})
	}
	return contains
}

func retirementCatalogItemIsExactLink(item *ast.ListItem, source []byte, relative, pagePath string) bool {
	paragraph, ok := item.FirstChild().(*ast.Paragraph)
	if !ok || paragraph.NextSibling() != nil {
		return false
	}
	link, ok := paragraph.FirstChild().(*ast.Link)
	if !ok || link.NextSibling() != nil || link.Reference != nil || !link.Title.IsEmpty() || len(link.Attributes()) != 0 {
		return false
	}
	target, external, valid := wiki.ResolveIndexDestination(relative, link.Destination.Value(source))
	if !valid || external || "wiki/"+target != pagePath {
		return false
	}
	var label strings.Builder
	for child := link.FirstChild(); child != nil; child = child.NextSibling() {
		plain, textNode := child.(*ast.Text)
		if !textNode || plain.SoftLineBreak() || plain.HardLineBreak() {
			return false
		}
		label.WriteString(plain.Value.Value(source))
	}
	return strings.TrimSpace(label.String()) != ""
}

func retirementCatalogReferencesPage(relative string, content []byte, pagePath string) (bool, error) {
	destinations, malformed := wiki.IndexDestinations(string(content), len(content)+1)
	if malformed {
		return false, ErrRetirementPlanInvalid
	}
	for _, destination := range destinations {
		target, external, valid := wiki.ResolveIndexDestination(relative, destination)
		if !valid {
			return false, ErrRetirementPlanInvalid
		}
		if !external && "wiki/"+target == pagePath {
			return true, nil
		}
	}
	return false, nil
}

func retirementListItemRange(item *ast.ListItem, source []byte) (int, int, bool) {
	start, end := len(source), -1
	_ = ast.Walk(item, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		block, ok := node.(ast.BlockNode)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		for _, segment := range block.Source() {
			start = min(start, segment.Start)
			end = max(end, segment.Stop)
		}
		return ast.WalkContinue, nil
	})
	if end < 0 {
		return 0, 0, false
	}
	for start > 0 && source[start-1] != '\n' {
		start--
	}
	for end < len(source) && source[end] != '\n' {
		end++
	}
	if end < len(source) {
		end++
	}
	return start, end, start < end
}

func retirementUnitFailure(pagePath, target, code string, cause error) error {
	diagnostics, err := NormalizeMaintenanceDiagnostics([]knowl.MaintenanceDiagnostic{{Code: code, Path: pagePath, Target: target}})
	if err != nil {
		return errors.Join(cause, ErrMaintenanceDiagnosticInvalid)
	}
	return &RetirementUnitError{Code: code, Diagnostics: diagnostics, Cause: cause}
}
