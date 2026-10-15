package fs

import (
	"context"
	"errors"
	"mime"
	"net/url"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
	"gopkg.in/yaml.v3"
)

const operatorPlainText = "text/plain"

var _ app.WorkspaceReader = (*Workspace)(nil)

// Page reads one detached concept and its scoped accepted provenance, or a
// canonical index, under one publication lock. It never fetches upstream resources.
func (workspace *Workspace) Page(ctx context.Context, scope knowl.ScopeRef, id knowl.PageID, limits knowl.ReadLimits) (knowl.OperatorPage, error) {
	relative, err := operatorPagePath(id, okf.DocumentConcept, okf.DocumentIndex)
	if err != nil {
		return knowl.OperatorPage{}, err
	}
	unlock, err := workspace.operatorLock(ctx, scope)
	if err != nil {
		return knowl.OperatorPage{}, err
	}
	defer unlock()
	wiki, err := openReadRoot(workspace.root, workspaceWikiDir)
	if err != nil {
		return knowl.OperatorPage{}, operatorReadError(err, app.ErrPageNotFound)
	}
	defer wiki.close()
	if kind, _ := okf.ClassifyPath(relative); kind == okf.DocumentIndex {
		ceiling := app.DefaultCatalogLimits().MaxCatalogBytes
		content, _, err := wiki.read(relative, limits, min(workspace.maxSourceBytes, ceiling))
		if err != nil {
			return knowl.OperatorPage{}, operatorReadError(err, app.ErrPageNotFound)
		}
		index, err := okf.ValidateIndex(relative, content, okfLimits(ceiling))
		if err != nil {
			return knowl.OperatorPage{}, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
		}
		digest := digestBytes(content)
		return knowl.OperatorPage{ID: id, Title: markdownTitle([]byte(index.Body)), Markdown: string(content), Digest: digest, Version: digest,
			RelatedPageIDs: []knowl.PageID{}, Sources: []knowl.OperatorPageSource{},
		}, nil
	}
	page, _, err := workspace.operatorPage(wiki, relative, limits)
	if err != nil {
		return knowl.OperatorPage{}, operatorReadError(err, app.ErrPageNotFound)
	}
	sources, err := workspace.operatorSources(ctx, scope)
	if err != nil {
		return knowl.OperatorPage{}, err
	}
	metadata := page.OKF
	result := knowl.OperatorPage{ID: page.ID, Title: page.Title, Markdown: page.Content, Digest: page.Digest, Version: page.Digest,
		Metadata:       &knowl.OperatorPageMetadata{Type: metadata.Type, Description: metadata.Description, Tags: slices.Clone(metadata.Tags), Status: string(metadata.ResolvedStatus), TrustTier: string(metadata.TrustTier), Stale: metadata.Stale},
		RelatedPageIDs: []knowl.PageID{}, Sources: []knowl.OperatorPageSource{},
	}
	for _, link := range markdownLinks(page.ID, []byte(page.Body)) {
		if _, err := operatorPagePath(link.To, okf.DocumentConcept); err == nil {
			result.RelatedPageIDs = append(result.RelatedPageIDs, link.To)
		}
	}
	slices.Sort(result.RelatedPageIDs)
	result.RelatedPageIDs = slices.Compact(result.RelatedPageIDs)
	for _, ref := range page.SourceRefs {
		source, exists := sources[ref]
		if !exists {
			continue
		}
		result.Sources = append(result.Sources, knowl.OperatorPageSource{SourceRef: ref, SourceID: source.SourceDocument.SourceID, DocumentID: source.SourceDocument.DocumentID, Revision: source.Version.Version, OriginalURI: operatorSafeURI(source.SourceDocument.URI)})
	}
	return result, nil
}

// SourceRevision resolves an exact opaque accepted reference in the trusted
// scope, then verifies only that immutable source's bytes against its manifest.
func (workspace *Workspace) SourceRevision(ctx context.Context, scope knowl.ScopeRef, ref string, limits knowl.ReadLimits) (knowl.OperatorSourceRevision, error) {
	if !operatorSourceRefValid(ref) {
		return knowl.OperatorSourceRevision{}, app.ErrOperatorInvalidRequest
	}
	unlock, err := workspace.operatorLock(ctx, scope)
	if err != nil {
		return knowl.OperatorSourceRevision{}, err
	}
	defer unlock()
	raw, err := openReadRoot(workspace.root, workspaceRawDir)
	if err != nil {
		return knowl.OperatorSourceRevision{}, operatorReadError(err, app.ErrOperatorSourceRevisionNotFound)
	}
	defer raw.close()
	sources, err := operatorSourceManifests(ctx, raw, scope)
	if err != nil {
		return knowl.OperatorSourceRevision{}, err
	}
	source, exists := sources[ref]
	if !exists {
		return knowl.OperatorSourceRevision{}, app.ErrOperatorSourceRevisionNotFound
	}
	relative := strings.TrimPrefix(path.Dir(source.ManifestRef), workspaceRawDir+"/") + "/source"
	content, _, err := raw.read(relative, limits, workspace.maxSourceBytes)
	if err != nil {
		return knowl.OperatorSourceRevision{}, operatorReadError(err, app.ErrOperatorSourceRevisionNotFound)
	}
	if digestBytes(content) != source.Version.Digest {
		return knowl.OperatorSourceRevision{}, errors.Join(app.ErrOperatorWorkspaceUnavailable, ErrDigestMismatch)
	}
	media, _, err := mime.ParseMediaType(source.MediaType)
	if err != nil || (media != operatorPlainText && media != "text/markdown") || !utf8.Valid(content) {
		return knowl.OperatorSourceRevision{}, app.ErrOperatorUnsupportedFormat
	}
	return knowl.OperatorSourceRevision{SourceRef: ref, Source: source.Source, Version: source.Version, MediaType: source.MediaType, Digest: source.Version.Digest, OriginalURI: operatorSafeURI(source.SourceDocument.URI), Text: string(content)}, nil
}

func (workspace *Workspace) operatorLock(ctx context.Context, scope knowl.ScopeRef) (func(), error) {
	if strings.TrimSpace(string(scope)) == "" {
		return nil, app.ErrOperatorInvalidRequest
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	unlock, err := workspace.lock(ctx)
	if err != nil {
		return nil, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	if err := workspace.checkPublishedLocked(); err != nil {
		unlock()
		return nil, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	return unlock, nil
}

func operatorReadError(err, missing error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrNotExist):
		return missing
	case errors.Is(err, app.ErrOperatorReadLimitExceeded), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return errors.Join(app.ErrOperatorWorkspaceUnavailable, err)
	}
}

func operatorPagePath(id knowl.PageID, wanted ...okf.DocumentKind) (string, error) {
	if !validReadPath(string(id)) || knowlwiki.NormalizePageTarget(string(id)) != string(id) {
		return "", app.ErrOperatorInvalidRequest
	}
	relative := string(id) + markdownExt
	kind, err := okf.ClassifyPath(relative)
	if err != nil || !slices.Contains(wanted, kind) {
		return "", app.ErrOperatorInvalidRequest
	}
	return relative, nil
}

func (workspace *Workspace) operatorPage(wiki *readRoot, relative string, limits knowl.ReadLimits) (knowl.PageSnapshot, os.FileInfo, error) {
	content, info, err := wiki.read(relative, limits, workspace.maxSourceBytes)
	if err != nil {
		return knowl.PageSnapshot{}, nil, err
	}
	document, err := okf.ParseConcept(relative, content, okfLimits(workspace.maxSourceBytes))
	if err != nil {
		return knowl.PageSnapshot{}, nil, err
	}
	id, valid := knowlwiki.PageIDFromPath(workspaceWikiDir + "/" + relative)
	if !valid {
		return knowl.PageSnapshot{}, nil, app.ErrOperatorWorkspaceUnavailable
	}
	page, err := parsedPageSnapshot(id, workspaceWikiDir+"/"+relative, content, digestBytes(content), info.ModTime(), document, workspace.now().UTC())
	return page, info, err
}

func (workspace *Workspace) operatorSources(ctx context.Context, scope knowl.ScopeRef) (map[string]knowl.AcceptedSource, error) {
	raw, err := openReadRoot(workspace.root, workspaceRawDir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]knowl.AcceptedSource{}, nil
	}
	if err != nil {
		return nil, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
	}
	defer raw.close()
	return operatorSourceManifests(ctx, raw, scope)
}

// ResolveAcceptedSources authenticates exact source refs against the
// workspace's immutable accepted manifests. Each validated citation identity
// is hashed into its deterministic manifest address; its components are never
// used directly as filesystem path segments.
func (workspace *Workspace) ResolveAcceptedSources(ctx context.Context, scope knowl.ScopeRef, refs []string) (map[string]knowl.AcceptedSource, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	plan, err := prepareAcceptedSourceResolveRequests(ctx, scope, refs)
	if err != nil {
		return nil, err
	}
	unlock, err := workspace.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := workspace.checkPublishedLocked(); err != nil {
		return nil, err
	}
	raw, err := openReadRoot(workspace.root, workspaceRawDir)
	if err != nil {
		return nil, err
	}
	defer raw.close()
	outcomes := make(map[string]acceptedSourceResolveOutcome, len(plan.probes))
	remaining := app.MaxAcceptedSourceResolveBytes
	for _, relative := range plan.probes {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		if remaining <= 0 {
			file, openErr := raw.open(relative, false)
			if err := contextErr(ctx); err != nil {
				if file != nil {
					_ = file.Close()
				}
				return nil, err
			}
			if errors.Is(openErr, os.ErrNotExist) {
				outcomes[relative] = acceptedSourceResolveOutcome{}
				continue
			}
			if openErr != nil {
				return nil, operatorReadError(openErr, app.ErrOperatorWorkspaceUnavailable)
			}
			_ = file.Close()
			if err := contextErr(ctx); err != nil {
				return nil, err
			}
			return nil, app.ErrOperatorReadLimitExceeded
		}
		content, _, readErr := raw.read(relative, knowl.ReadLimits{Bytes: remaining}, maxStageManifestBytes)
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		if errors.Is(readErr, os.ErrNotExist) {
			outcomes[relative] = acceptedSourceResolveOutcome{}
			continue
		}
		if readErr != nil {
			return nil, operatorReadError(readErr, app.ErrOperatorWorkspaceUnavailable)
		}
		remaining -= len(content)
		var manifest sourceManifest
		if yaml.Unmarshal(content, &manifest) != nil {
			return nil, app.ErrSourceInvalid
		}
		source, acceptedErr := manifest.accepted()
		if acceptedErr != nil || !validSourceManifest(manifest) || app.ValidateAcceptedSourceDescriptor(source) != nil ||
			source.Scope != scope || source.ManifestRef != workspaceRawDir+"/"+relative {
			return nil, app.ErrSourceInvalid
		}
		outcomes[relative] = acceptedSourceResolveOutcome{source: source, found: true}
	}

	result := make(map[string]knowl.AcceptedSource, len(refs))
	resolvedManifests := make(map[string]string, len(refs))
	for _, request := range plan.requests {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		matches := make([]knowl.AcceptedSource, 0, 1)
		for _, candidate := range request.candidates {
			outcome := outcomes[candidate.relative]
			if !outcome.found {
				continue
			}
			source := outcome.source
			if source.Source != candidate.identity.Source || source.Version.Version != candidate.identity.Revision ||
				!app.SourceRefMatches(request.ref, source) {
				return nil, app.ErrSourceInvalid
			}
			matches = append(matches, source)
		}
		switch len(matches) {
		case 0:
			return nil, &app.SourceRefNotFoundError{Ref: request.ref}
		case 1:
			source := matches[0]
			if _, duplicate := resolvedManifests[source.ManifestRef]; duplicate {
				return nil, &app.SourceRefDuplicateError{Ref: request.ref}
			}
			resolvedManifests[source.ManifestRef] = request.ref
			result[request.ref] = source
		default:
			return nil, app.ErrSourceRefAmbiguous
		}
	}
	return result, nil
}

type acceptedSourceResolveCandidate struct {
	identity app.SourceRefIdentity
	relative string
}

type acceptedSourceResolveRequest struct {
	ref        string
	candidates []acceptedSourceResolveCandidate
}

type acceptedSourceResolvePlan struct {
	requests []acceptedSourceResolveRequest
	probes   []string
}

type acceptedSourceResolveOutcome struct {
	source knowl.AcceptedSource
	found  bool
}

// prepareAcceptedSourceResolveRequests performs all pure parsing and address
// derivation before the publication lock is acquired.
func prepareAcceptedSourceResolveRequests(ctx context.Context, scope knowl.ScopeRef, refs []string) (acceptedSourceResolvePlan, error) {
	if strings.TrimSpace(string(scope)) == "" || len(refs) == 0 || len(refs) > app.MaxAcceptedSourceResolveRefs {
		return acceptedSourceResolvePlan{}, app.ErrSourceInvalid
	}
	ordered := slices.Clone(refs)
	sort.Strings(ordered)
	plan := acceptedSourceResolvePlan{requests: make([]acceptedSourceResolveRequest, 0, len(ordered))}
	probePaths := make(map[string]struct{}, app.MaxAcceptedSourceResolveProbes)
	for index, ref := range ordered {
		if err := contextErr(ctx); err != nil {
			return acceptedSourceResolvePlan{}, err
		}
		if index > 0 && ordered[index-1] == ref {
			return acceptedSourceResolvePlan{}, app.ErrSourceInvalid
		}
		identities, err := app.ParseSourceRefCandidates(ref)
		if err != nil {
			return acceptedSourceResolvePlan{}, err
		}
		request := acceptedSourceResolveRequest{ref: ref, candidates: make([]acceptedSourceResolveCandidate, 0, len(identities))}
		for _, identity := range identities {
			if err := contextErr(ctx); err != nil {
				return acceptedSourceResolvePlan{}, err
			}
			relative, err := acceptedSourceManifestRelative(scope, identity)
			if err != nil {
				return acceptedSourceResolvePlan{}, app.ErrSourceInvalid
			}
			request.candidates = append(request.candidates, acceptedSourceResolveCandidate{identity: identity, relative: relative})
			if _, exists := probePaths[relative]; exists {
				continue
			}
			if len(plan.probes) >= app.MaxAcceptedSourceResolveProbes {
				return acceptedSourceResolvePlan{}, app.ErrSourceRefLimit
			}
			probePaths[relative] = struct{}{}
			plan.probes = append(plan.probes, relative)
		}
		plan.requests = append(plan.requests, request)
	}
	return plan, nil
}

// operatorSourceManifests reads bounded manifests only. A source ref is compared
// to accepted identities, never interpreted as an address or a file path.
func operatorSourceManifests(ctx context.Context, raw *readRoot, scope knowl.ScopeRef) (map[string]knowl.AcceptedSource, error) {
	sources := make(map[string]knowl.AcceptedSource)
	ambiguous := make(map[string]struct{})
	remaining := app.DefaultCatalogLimits().MaxSnapshotBytes
	err := raw.walk(func(relative string, entry os.DirEntry) error {
		if err := contextErr(ctx); err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "manifest.yaml" {
			return nil
		}
		if remaining <= 0 {
			return app.ErrOperatorReadLimitExceeded
		}
		content, _, err := raw.read(relative, knowl.ReadLimits{Bytes: remaining}, maxStageManifestBytes)
		if err != nil {
			return err
		}
		remaining -= len(content)
		var manifest sourceManifest
		if err := yaml.Unmarshal(content, &manifest); err != nil {
			return app.ErrOperatorWorkspaceUnavailable
		}
		if manifest.Scope != string(scope) {
			return nil
		}
		source, acceptedErr := manifest.accepted()
		if acceptedErr != nil || !validSourceManifest(manifest) || source.ManifestRef != workspaceRawDir+"/"+relative {
			return app.ErrOperatorWorkspaceUnavailable
		}
		if len(app.SourceRefAliases(source)) == 0 {
			return app.ErrOperatorWorkspaceUnavailable
		}
		addSourceRefAliases(sources, ambiguous, source)
		return nil
	})
	return sources, operatorReadError(err, app.ErrOperatorWorkspaceUnavailable)
}

func operatorSourceRefValid(ref string) bool {
	_, err := app.ParseSourceRefCandidates(ref)
	return err == nil
}

func operatorSafeURI(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Opaque != "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}
