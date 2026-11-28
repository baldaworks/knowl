package mcpsdk_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/baldaworks/knowl/internal/mcpsdk"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowlmcp "github.com/baldaworks/knowl/pkg/knowl/mcp"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testSkillsExtensionID = "io.modelcontextprotocol/skills"
	testSkillURI          = "skill://knowl/SKILL.md"
	testProtocolVersion   = "2026-07-28"
	testPublicCacheScope  = "public"
)

type testListSkillsParams struct {
	sdkmcp.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}

type testGetSkillParams struct {
	sdkmcp.ParamsBase
	URI string `json:"uri"`
}

type testSkillResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type testSkillEntry struct {
	URI         string              `json:"uri"`
	Frontmatter map[string]any      `json:"frontmatter"`
	Resources   []testSkillResource `json:"resources"`
}

type testListSkillsResult struct {
	sdkmcp.ResultBase
	sdkmcp.Cacheable
	ResultType string           `json:"resultType"`
	Skills     []testSkillEntry `json:"skills"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type testGetSkillResult struct {
	sdkmcp.ResultBase
	sdkmcp.Cacheable
	ResultType string         `json:"resultType"`
	Skill      testSkillEntry `json:"skill"`
}

func TestSkillsExtensionPublishesCanonicalSkillAcrossFacades(t *testing.T) {
	registry := newSkillTestRegistry(t)
	readOnly := connectSkillTestServer(t, registry, true)
	readWrite := connectSkillTestServer(t, registry, false)

	readOnlyEntry, readOnlyContent := assertSkillSurface(t, readOnly)
	readWriteEntry, readWriteContent := assertSkillSurface(t, readWrite)
	if readOnlyEntry.Resources[0] != readWriteEntry.Resources[0] ||
		readOnlyEntry.URI != readWriteEntry.URI ||
		readOnlyEntry.Frontmatter["name"] != readWriteEntry.Frontmatter["name"] ||
		readOnlyEntry.Frontmatter["description"] != readWriteEntry.Frontmatter["description"] {
		t.Fatalf("facade skill entries differ: read-only %#v, read-write %#v", readOnlyEntry, readWriteEntry)
	}
	if readOnlyContent != readWriteContent {
		t.Fatal("facade skill bytes differ")
	}

	readOnlyTools, err := readOnly.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readWriteTools, err := readWrite.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasTool(readOnlyTools, "knowl_ingest") || !hasTool(readWriteTools, "knowl_ingest") {
		t.Fatal("skill publication changed facade tool filtering")
	}
}

func TestSkillsExtensionRejectsInvalidCursorAndUnknownURIs(t *testing.T) {
	session := connectSkillTestServer(t, newSkillTestRegistry(t), true)

	_, err := sdkmcp.CallCustomMethod[*testListSkillsParams, *testListSkillsResult](
		t.Context(), session, "skills/list", &testListSkillsParams{Cursor: "unsupported"})
	assertInvalidParams(t, err)
	_, err = sdkmcp.CallCustomMethod[*testGetSkillParams, *testGetSkillResult](
		t.Context(), session, "skills/get", &testGetSkillParams{URI: "skill://other/SKILL.md"})
	assertInvalidParams(t, err)
	_, err = session.ReadResource(t.Context(), &sdkmcp.ReadResourceParams{URI: "skill://knowl/missing.md"})
	assertInvalidParams(t, err)
}

func assertSkillSurface(t *testing.T, session *sdkmcp.ClientSession) (testSkillEntry, string) {
	t.Helper()
	capabilities := session.InitializeResult().Capabilities
	settings, ok := capabilities.Extensions[testSkillsExtensionID].(map[string]any)
	if !ok || len(settings) != 0 || capabilities.Resources == nil {
		t.Fatalf("skill capabilities = %#v", capabilities)
	}

	listed, err := sdkmcp.CallCustomMethod[*testListSkillsParams, *testListSkillsResult](
		t.Context(), session, "skills/list", &testListSkillsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if listed.ResultType != "complete" || listed.CacheScope != testPublicCacheScope || listed.TTLMs != 0 ||
		listed.NextCursor != "" || len(listed.Skills) != 1 {
		t.Fatalf("skills/list = %#v", listed)
	}
	entry := listed.Skills[0]
	if entry.URI != testSkillURI || entry.Frontmatter["name"] != "knowl" ||
		entry.Frontmatter["description"] == "" || len(entry.Frontmatter) != 2 || len(entry.Resources) != 1 {
		t.Fatalf("skill entry = %#v", entry)
	}
	resource := entry.Resources[0]
	if resource.URI != testSkillURI || len(resource.Digest) != len("sha256:")+64 ||
		resource.Digest[:len("sha256:")] != "sha256:" || resource.Size <= 0 {
		t.Fatalf("skill resource = %#v", resource)
	}

	got, err := sdkmcp.CallCustomMethod[*testGetSkillParams, *testGetSkillResult](
		t.Context(), session, "skills/get", &testGetSkillParams{URI: testSkillURI})
	if err != nil {
		t.Fatal(err)
	}
	if got.ResultType != "complete" || got.CacheScope != testPublicCacheScope || got.TTLMs != 0 ||
		got.Skill.Resources[0] != resource || got.Skill.URI != entry.URI {
		t.Fatalf("skills/get = %#v", got)
	}

	read, err := session.ReadResource(t.Context(), &sdkmcp.ReadResourceParams{URI: testSkillURI})
	if err != nil {
		t.Fatal(err)
	}
	if read.CacheScope != testPublicCacheScope || read.TTLMs != 0 || len(read.Contents) != 1 ||
		read.Contents[0].URI != testSkillURI || read.Contents[0].MIMEType != "text/markdown" ||
		int64(len([]byte(read.Contents[0].Text))) != resource.Size {
		t.Fatalf("resources/read = %#v", read)
	}
	digest := sha256.Sum256([]byte(read.Contents[0].Text))
	if resource.Digest != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("resource digest = %q", resource.Digest)
	}
	listedResources, err := session.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listedResources.Resources) != 1 || listedResources.Resources[0].URI != testSkillURI ||
		listedResources.Resources[0].Name != "knowl" || listedResources.Resources[0].MIMEType != "text/markdown" {
		t.Fatalf("resources/list = %#v", listedResources)
	}
	return entry, read.Contents[0].Text
}

func connectSkillTestServer(t *testing.T, registry *knowlmcp.Server, readOnly bool) *sdkmcp.ClientSession {
	t.Helper()
	server, err := mcpsdk.NewServerWithOptions(registry, mcpsdk.ServerOptions{ReadOnly: readOnly})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, serverTransport) }()

	clientCapabilities := &sdkmcp.ClientCapabilities{}
	clientCapabilities.AddExtension(testSkillsExtensionID, nil)
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "skills-test", Version: "1"},
		&sdkmcp.ClientOptions{Capabilities: clientCapabilities},
	)
	if err := sdkmcp.AddSendingCustomMethod[*testListSkillsParams, *testListSkillsResult](client, "skills/list"); err != nil {
		t.Fatal(err)
	}
	if err := sdkmcp.AddSendingCustomMethod[*testGetSkillParams, *testGetSkillResult](client, "skills/get"); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(ctx, clientTransport, &sdkmcp.ClientSessionOptions{ProtocolVersion: testProtocolVersion})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("server shutdown: %v", err)
		}
	})
	return session
}

func newSkillTestRegistry(t *testing.T) *knowlmcp.Server {
	t.Helper()
	query, err := app.NewQueryService(sdkPageContent{}, sdkPageOperations{}, sdkPageIndex{}, nil, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := knowlmcp.NewServer(query, new(app.IngestService), sdkWaker{}, domain.ScopeRef("local"), domain.ReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func hasTool(result *sdkmcp.ListToolsResult, name string) bool {
	for _, tool := range result.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func assertInvalidParams(t *testing.T, err error) {
	t.Helper()
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("error = %v, want invalid params", err)
	}
}
