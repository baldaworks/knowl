package knowl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	multiScopeDefaultRef  = domain.ScopeRef("legacy-project")
	multiScopePlatformRef = domain.ScopeRef("project-platform")
	multiScopePlatform    = domain.ScopeName("platform")
	multiScopeAdapter     = "fixture"
	multiScopeMediaType   = "text/plain"
)

type scopeCaptureMaintainer struct{ input domain.MaintenanceInput }

type scopePageMaintainer struct{}

func (scopePageMaintainer) Plan(_ context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	const pagePath = "wiki/concepts/shared.md"
	ref := app.SourceRefKey(input.Source)
	content := fmt.Sprintf("---\nid: concepts/shared\ntitle: Shared identity\ntype: topic\nsource_refs:\n  - %s\n---\n# Shared identity\n\n%s\n", ref, input.SourceText)
	plan := domain.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   []string{ref},
		Edits:        []domain.FileEdit{{Path: pagePath, Content: []byte(content)}},
	}
	for _, catalog := range input.Catalogs {
		if catalog.Path == testIndexPath {
			plan.CatalogAdditions = []domain.CatalogAddition{{Path: catalog.Path, ExpectedDigest: catalog.Digest, Children: []string{pagePath}}}
			break
		}
	}
	return plan, nil
}

type scopeCaptureAuthorizer struct {
	scope domain.ScopeRef
	kind  app.OperationKind
}

func (authorizer *scopeCaptureAuthorizer) Authorize(_ context.Context, scope domain.ScopeRef, kind app.OperationKind) error {
	authorizer.scope, authorizer.kind = scope, kind
	return nil
}

func (maintainer *scopeCaptureMaintainer) Plan(_ context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	maintainer.input = input
	return domain.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   []string{app.SourceRefKey(input.Source)},
	}, nil
}

func TestHostComposesSharedServicesAcrossScopeWorkspaces(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.StorePath = filepath.Join(config.Workspace, ".knowl", "state.db")
	config.ListenAddr = operatorRuntimeAddress
	config.DefaultScopeName = DefaultPublicScope
	config.Scopes = []ScopeConfig{
		{Name: DefaultPublicScope, Ref: multiScopeDefaultRef},
		{Name: multiScopePlatform, Ref: multiScopePlatformRef},
	}
	config.IngestOptions.AutoApply = true
	authorizer := &scopeCaptureAuthorizer{}
	config.ScopeAuthorizer = authorizer
	maintainer := &scopeCaptureMaintainer{}
	host, err := NewHost(t.Context(), config, maintainer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()

	type fixture struct {
		ref     domain.ScopeRef
		content string
	}
	var submissions []app.IngestSubmission
	for _, item := range []fixture{{multiScopeDefaultRef, "default evidence"}, {multiScopePlatformRef, "platform evidence"}} {
		sum := sha256.Sum256([]byte(item.content))
		submission, submitErr := host.service.Submit(t.Context(), domain.SourceEnvelope{
			Scope: item.ref, Source: domain.SourceRef{Adapter: multiScopeAdapter, ID: "same-source"},
			Version:   domain.SourceVersion{Version: "1", Digest: hex.EncodeToString(sum[:])},
			MediaType: multiScopeMediaType, Content: []byte(item.content),
		})
		if submitErr != nil {
			t.Fatalf("submit scope %q: %v", item.ref, submitErr)
		}
		submissions = append(submissions, submission)
	}
	if submissions[0].Operation.ID == submissions[1].Operation.ID {
		t.Fatalf("scope-bound operations collided: %q", submissions[0].Operation.ID)
	}
	selected, err := host.MCP().Call(t.Context(), "knowl_operation", map[string]any{"id": string(submissions[1].Operation.ID), "scope": string(multiScopePlatform)})
	if err != nil || selected.(mcp.OperationResult).ID != submissions[1].Operation.ID || authorizer.scope != multiScopePlatformRef || authorizer.kind != app.OperationRead {
		t.Fatalf("selected MCP operation = %#v, auth=(%q,%q), err=%v", selected, authorizer.scope, authorizer.kind, err)
	}
	if _, err = host.MCP().Call(t.Context(), "knowl_operation", map[string]any{"id": string(submissions[1].Operation.ID)}); !errors.Is(err, app.ErrOperationNotFound) {
		t.Fatalf("default scope disclosed platform operation: %v", err)
	}
	for index, item := range []fixture{{multiScopeDefaultRef, "default evidence"}, {multiScopePlatformRef, "platform evidence"}} {
		operation, readErr := host.operations.Operation(t.Context(), item.ref, submissions[index].Operation.ID)
		if readErr != nil || operation.ID != submissions[index].Operation.ID {
			t.Fatalf("read scope %q operation: %+v %v", item.ref, operation, readErr)
		}
		other := submissions[1-index].Operation.ID
		if _, readErr = host.operations.Operation(t.Context(), item.ref, other); !errors.Is(readErr, app.ErrOperationNotFound) {
			t.Fatalf("cross-scope operation read for %q: %v", item.ref, readErr)
		}
	}
	defaultWorkspace, err := host.workspaces.Resolve(multiScopeDefaultRef)
	if err != nil {
		t.Fatal(err)
	}
	platformWorkspace, err := host.workspaces.Resolve(multiScopePlatformRef)
	if err != nil {
		t.Fatal(err)
	}
	if defaultWorkspace.Root == platformWorkspace.Root {
		t.Fatal("scope workspaces share a canonical root")
	}
	committers := host.retirementCommitter.(*scopedRetirementCommitter)
	releaseDefault, err := committers.byScope[multiScopeDefaultRef].coordinator.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDefault()
	otherCtx, cancelOther := context.WithTimeout(t.Context(), time.Second)
	releasePlatform, err := committers.byScope[multiScopePlatformRef].coordinator.Acquire(otherCtx)
	cancelOther()
	if err != nil {
		t.Fatalf("disjoint scope publication gate blocked: %v", err)
	}
	releasePlatform()
	sameCtx, cancelSame := context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, err = committers.byScope[multiScopeDefaultRef].coordinator.Acquire(sameCtx)
	cancelSame()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-scope publication overlapped: %v", err)
	}
	if _, err = host.content.Snapshot(t.Context(), multiScopeDefaultRef); err != nil {
		t.Fatal(err)
	}
	if _, err = host.content.Snapshot(t.Context(), multiScopePlatformRef); err != nil {
		t.Fatal(err)
	}
	isolatedText := []byte("platform-only maintenance input")
	isolatedDigest := sha256.Sum256(isolatedText)
	result, err := host.service.Ingest(t.Context(), domain.SourceEnvelope{
		Scope: multiScopePlatformRef, Source: domain.SourceRef{Adapter: multiScopeAdapter, ID: "captured-source"},
		Version:   domain.SourceVersion{Version: "1", Digest: hex.EncodeToString(isolatedDigest[:])},
		MediaType: multiScopeMediaType, Content: isolatedText,
	})
	if err != nil || result.Operation.Status != domain.StatusCommitted {
		t.Fatalf("capture maintenance input: %v", err)
	}
	if maintainer.input.Scope != multiScopePlatformRef || maintainer.input.Schema.Scope != multiScopePlatformRef || maintainer.input.Source.Scope != multiScopePlatformRef || maintainer.input.SourceText != string(isolatedText) {
		t.Fatalf("maintainer input crossed scope boundary: %+v", maintainer.input)
	}
	platformSnapshot, err := host.content.Snapshot(t.Context(), multiScopePlatformRef)
	if err != nil || len(platformSnapshot.Pages) != 0 {
		t.Fatalf("platform terminal snapshot = %+v, %v", platformSnapshot.Pages, err)
	}
	defaultSnapshot, err := host.content.Snapshot(t.Context(), multiScopeDefaultRef)
	if err != nil || len(defaultSnapshot.Pages) != 0 {
		t.Fatalf("default snapshot leaked platform page = %+v, %v", defaultSnapshot.Pages, err)
	}
}

func TestHostIsolatesNamedScopeValidationFailure(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "scopes", string(multiScopePlatform))
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "raw"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.Workspace = root
	config.StorePath = filepath.Join(root, ".knowl", "state.db")
	config.DefaultScopeName = DefaultPublicScope
	config.Scopes = []ScopeConfig{
		{Name: DefaultPublicScope, Ref: "healthy-default"},
		{Name: multiScopePlatform, Ref: "broken-platform"},
	}
	host, err := NewHost(t.Context(), config, provider.Fixture{})
	if err != nil {
		t.Fatalf("healthy scope should keep host composable: %v", err)
	}
	defer func() { _ = host.Close() }()
	states := host.ScopesReadiness()
	if len(states) != 2 || states[0] != (ScopeReadiness{Name: DefaultPublicScope, Ready: true}) || states[1] != (ScopeReadiness{Name: multiScopePlatform, Reason: scopeReadinessUnavailable}) {
		t.Fatalf("scope readiness = %+v", states)
	}
	if _, err = host.content.Snapshot(t.Context(), "healthy-default"); err != nil {
		t.Fatal(err)
	}
	if _, err = host.content.Snapshot(t.Context(), "broken-platform"); !errors.Is(err, ErrScopeUnavailable) {
		t.Fatalf("broken scope read = %v", err)
	}
}

func TestHostOperatorReadersRouteNamedScope(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.StorePath = filepath.Join(config.Workspace, ".knowl", "state.db")
	config.Scopes = []ScopeConfig{
		{Name: DefaultPublicScope, Ref: multiScopeDefaultRef},
		{Name: multiScopePlatform, Ref: multiScopePlatformRef},
	}
	host, err := NewHost(t.Context(), config, scopePageMaintainer{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()

	sources, err := newOperatorSourceReader(host).ListSources(t.Context(), multiScopePlatformRef, app.OperatorReadOptions{Limit: 10})
	if err != nil || len(sources.Items) != 0 {
		t.Fatalf("named-scope source reader=%+v err=%v", sources, err)
	}
	statisticsReader, ok := host.operations.(app.StatisticsReader)
	if !ok {
		t.Fatal("operation store does not expose statistics")
	}
	statistics, err := (&operatorStatisticsReader{host: host, reader: statisticsReader}).Statistics(t.Context(), multiScopePlatformRef)
	if err != nil || statistics.Product.ConfiguredSources != 0 {
		t.Fatalf("named-scope statistics=%+v err=%v", statistics, err)
	}
	_, err = (operatorRetirementReader{host: host}).SourceRetirement(t.Context(), multiScopePlatformRef, domain.SourceOwner{Adapter: domain.SourceAdapterFilesystem, SourceID: testMissingName})
	if errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("named-scope retirement reader rejected configured scope: %v", err)
	}
	if _, err := newOperatorSourceReader(host).ListSources(t.Context(), "unknown", app.OperatorReadOptions{Limit: 10}); !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("unknown source scope error=%v", err)
	}
}

func TestHostMaintainsRetrievesExportsAndRestartsCollidingScopeContent(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.StorePath = filepath.Join(config.Workspace, ".knowl", "state.db")
	config.DefaultScopeName = DefaultPublicScope
	config.Scopes = []ScopeConfig{
		{Name: DefaultPublicScope, Ref: multiScopeDefaultRef},
		{Name: multiScopePlatform, Ref: multiScopePlatformRef},
	}
	config.IngestOptions.AutoApply = true

	host, err := NewHost(t.Context(), config, scopePageMaintainer{})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = host.Close()
		}
	}()

	fixtures := []struct {
		ref    domain.ScopeRef
		marker string
	}{
		{ref: multiScopeDefaultRef, marker: "defaultscopebeacon"},
		{ref: multiScopePlatformRef, marker: "platformscopebeacon"},
	}
	for _, fixture := range fixtures {
		digest := sha256.Sum256([]byte(fixture.marker))
		result, ingestErr := host.service.Ingest(t.Context(), domain.SourceEnvelope{
			Scope:     fixture.ref,
			Source:    domain.SourceRef{Adapter: multiScopeAdapter, ID: "colliding-source"},
			Version:   domain.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])},
			MediaType: multiScopeMediaType,
			Content:   []byte(fixture.marker),
		})
		if ingestErr != nil || result.Operation.Status != domain.StatusCommitted {
			t.Fatalf("ingest scope %q: status=%q err=%v", fixture.ref, result.Operation.Status, ingestErr)
		}
	}

	for index, fixture := range fixtures {
		result, queryErr := host.query.Query(t.Context(), fixture.ref, fixture.marker, domain.ReadLimits{}, nil)
		if queryErr != nil || len(result.Pages) != 1 || result.Pages[0].ID != "concepts/shared" || !strings.Contains(result.Pages[0].Snippet, fixture.marker) {
			t.Fatalf("query scope %q: result=%+v err=%v", fixture.ref, result, queryErr)
		}
		foreign := fixtures[1-index].marker
		foreignResult, foreignErr := host.query.Query(t.Context(), fixture.ref, foreign, domain.ReadLimits{}, nil)
		if foreignErr != nil || len(foreignResult.Pages) != 0 {
			t.Fatalf("scope %q leaked marker %q: result=%+v err=%v", fixture.ref, foreign, foreignResult, foreignErr)
		}
		destination := filepath.Join(t.TempDir(), "export")
		if exportErr := host.content.ExportOKF(t.Context(), fixture.ref, destination, contentfs.DefaultExportLimits()); exportErr != nil {
			t.Fatalf("export scope %q: %v", fixture.ref, exportErr)
		}
		page, readErr := os.ReadFile(filepath.Join(destination, "concepts", "shared.md"))
		if readErr != nil || !strings.Contains(string(page), fixture.marker) || strings.Contains(string(page), foreign) {
			t.Fatalf("export scope %q page=%q err=%v", fixture.ref, page, readErr)
		}
	}
	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/retrieve?query=platformscopebeacon&scope=platform", nil)
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "platformscopebeacon") || strings.Contains(response.Body.String(), string(multiScopePlatformRef)) {
		t.Fatalf("selected HTTP retrieval status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response = httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	readiness := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(readiness, `"name":"default"`) || !strings.Contains(readiness, `"name":"platform"`) || strings.Contains(readiness, string(multiScopeDefaultRef)) || strings.Contains(readiness, config.Workspace) {
		t.Fatalf("public readiness status=%d body=%s", response.Code, readiness)
	}

	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	restarted, err := NewHost(t.Context(), config, scopePageMaintainer{})
	if err != nil {
		t.Fatalf("restart host: %v", err)
	}
	defer func() { _ = restarted.Close() }()
	for _, fixture := range fixtures {
		result, queryErr := restarted.query.Query(t.Context(), fixture.ref, fixture.marker, domain.ReadLimits{}, nil)
		if queryErr != nil || len(result.Pages) != 1 || !strings.Contains(result.Pages[0].Snippet, fixture.marker) {
			t.Fatalf("restart query scope %q: result=%+v err=%v", fixture.ref, result, queryErr)
		}
	}
}
