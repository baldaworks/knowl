package knowl_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	hostSkillsExtensionID = "io.modelcontextprotocol/skills"
	hostSkillURI          = "skill://knowl/SKILL.md"
	hostModernProtocol    = "2026-07-28"
)

type hostListSkillsParams struct {
	sdkmcp.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}

type hostGetSkillParams struct {
	sdkmcp.ParamsBase
	URI string `json:"uri"`
}

type hostSkillResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type hostSkillEntry struct {
	URI         string              `json:"uri"`
	Frontmatter map[string]any      `json:"frontmatter"`
	Resources   []hostSkillResource `json:"resources"`
}

type hostListSkillsResult struct {
	sdkmcp.ResultBase
	sdkmcp.Cacheable
	ResultType string           `json:"resultType"`
	Skills     []hostSkillEntry `json:"skills"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type hostGetSkillResult struct {
	sdkmcp.ResultBase
	sdkmcp.Cacheable
	ResultType string         `json:"resultType"`
	Skill      hostSkillEntry `json:"skill"`
}

type skillCountingMaintainer struct {
	calls atomic.Int64
}

func (maintainer *skillCountingMaintainer) Plan(context.Context, domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	maintainer.calls.Add(1)
	return domain.ModelEditPlan{}, errors.New("unexpected skill maintenance call")
}

func TestHTTPMCPSkillsMatchCanonicalFileAcrossFacades(t *testing.T) {
	canonical, err := os.ReadFile(filepath.Join(knowlRepositoryRoot(t), "internal", "mcpsdk", "skills", "knowl", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}

	type observed struct {
		entry   hostSkillEntry
		content string
	}
	results := make(map[knowl.AccessMode]observed, 2)
	for _, mode := range []knowl.AccessMode{knowl.AccessModeReadOnly, knowl.AccessModeReadWrite} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			workspace, workspaceErr := contentfs.New(t.TempDir())
			if workspaceErr != nil {
				t.Fatal(workspaceErr)
			}
			if workspaceErr = workspace.Init(); workspaceErr != nil {
				t.Fatal(workspaceErr)
			}
			config := knowl.DefaultConfig()
			config.Workspace = workspace.Root()
			config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
			config.ListenAddr = hostListenAddr
			config.OperatorToken = hostWriteToken
			config.MCP = knowl.FacadeConfig{AuthMode: knowl.AuthModeToken, AccessMode: mode}
			maintainer := new(skillCountingMaintainer)
			host, hostErr := knowl.NewHost(ctx, config, maintainer)
			if hostErr != nil {
				t.Fatal(hostErr)
			}
			defer shutdownHost(t, host)

			request := httptest.NewRequest(http.MethodPost, "http://knowl/mcp", nil)
			request.Header.Set("Authorization", "Bearer "+hostWriteToken)
			response := httptest.NewRecorder()
			host.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("pre-start MCP status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			if hostErr = host.Start(ctx); hostErr != nil {
				t.Fatal(hostErr)
			}

			unauthorized := newHostSkillsClient(t)
			if session, connectErr := unauthorized.Connect(ctx, &sdkmcp.StreamableClientTransport{
				Endpoint:             "http://" + host.Addr() + "/mcp",
				DisableStandaloneSSE: true,
			}, &sdkmcp.ClientSessionOptions{ProtocolVersion: hostModernProtocol}); connectErr == nil {
				_ = session.Close()
				t.Fatal("unauthorized Skills client connected")
			}

			session := connectHostSkillsClient(t, ctx, host, hostWriteToken)
			defer closeMCPClient(t, session)
			entry, content := assertHostSkillSurface(t, session, canonical)
			listed, listErr := session.ListTools(ctx, nil)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if hasHostTool(listed, hostIngestToolName) != (mode == knowl.AccessModeReadWrite) {
				t.Fatalf("tool inventory does not match %s facade", mode)
			}
			if calls := maintainer.calls.Load(); calls != 0 {
				t.Fatalf("skill requests invoked maintainer %d times", calls)
			}
			results[mode] = observed{entry: entry, content: content}
		})
	}
	readOnly, readWrite := results[knowl.AccessModeReadOnly], results[knowl.AccessModeReadWrite]
	if readOnly.content != readWrite.content || readOnly.entry.Resources[0] != readWrite.entry.Resources[0] ||
		readOnly.entry.Frontmatter["name"] != readWrite.entry.Frontmatter["name"] ||
		readOnly.entry.Frontmatter["description"] != readWrite.entry.Frontmatter["description"] {
		t.Fatalf("HTTP facade skill mismatch: read-only %#v, read-write %#v", readOnly.entry, readWrite.entry)
	}
}

func assertHostSkillSurface(t *testing.T, session *sdkmcp.ClientSession, canonical []byte) (hostSkillEntry, string) {
	t.Helper()
	capabilities := session.InitializeResult().Capabilities
	settings, ok := capabilities.Extensions[hostSkillsExtensionID].(map[string]any)
	if !ok || len(settings) != 0 || capabilities.Resources == nil {
		t.Fatalf("HTTP MCP capabilities = %#v", capabilities)
	}
	listed, err := sdkmcp.CallCustomMethod[*hostListSkillsParams, *hostListSkillsResult](
		t.Context(), session, "skills/list", &hostListSkillsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if listed.ResultType != "complete" || listed.CacheScope != "public" || listed.NextCursor != "" || len(listed.Skills) != 1 {
		t.Fatalf("HTTP skills/list = %#v", listed)
	}
	entry := listed.Skills[0]
	if entry.URI != hostSkillURI || len(entry.Resources) != 1 || entry.Resources[0].URI != hostSkillURI ||
		entry.Frontmatter["name"] != "knowl" || entry.Frontmatter["description"] == "" {
		t.Fatalf("HTTP skill entry = %#v", entry)
	}
	digest := sha256.Sum256(canonical)
	wantDigest := "sha256:" + hex.EncodeToString(digest[:])
	if entry.Resources[0].Digest != wantDigest || entry.Resources[0].Size != int64(len(canonical)) {
		t.Fatalf("HTTP skill manifest resource = %#v", entry.Resources[0])
	}
	got, err := sdkmcp.CallCustomMethod[*hostGetSkillParams, *hostGetSkillResult](
		t.Context(), session, "skills/get", &hostGetSkillParams{URI: hostSkillURI})
	if err != nil || got.Skill.Resources[0] != entry.Resources[0] || got.Skill.URI != entry.URI {
		t.Fatalf("HTTP skills/get = %#v, %v", got, err)
	}
	read, err := session.ReadResource(t.Context(), &sdkmcp.ReadResourceParams{URI: hostSkillURI})
	if err != nil || len(read.Contents) != 1 {
		t.Fatalf("HTTP resources/read = %#v, %v", read, err)
	}
	if read.Contents[0].Text != string(canonical) {
		t.Fatal("HTTP resource bytes differ from canonical Git skill")
	}
	_, err = sdkmcp.CallCustomMethod[*hostListSkillsParams, *hostListSkillsResult](
		t.Context(), session, "skills/list", &hostListSkillsParams{Cursor: "invalid"})
	assertHostInvalidParams(t, err)
	_, err = sdkmcp.CallCustomMethod[*hostGetSkillParams, *hostGetSkillResult](
		t.Context(), session, "skills/get", &hostGetSkillParams{URI: "skill://unknown/SKILL.md"})
	assertHostInvalidParams(t, err)
	return entry, read.Contents[0].Text
}

func connectHostSkillsClient(t *testing.T, ctx context.Context, host *knowl.Host, token string) *sdkmcp.ClientSession {
	t.Helper()
	client := newHostSkillsClient(t)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             "http://" + host.Addr() + "/mcp",
		HTTPClient:           mcpHTTPClient(token),
		DisableStandaloneSSE: true,
	}, &sdkmcp.ClientSessionOptions{ProtocolVersion: hostModernProtocol})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func newHostSkillsClient(t *testing.T) *sdkmcp.Client {
	t.Helper()
	capabilities := &sdkmcp.ClientCapabilities{}
	capabilities.AddExtension(hostSkillsExtensionID, nil)
	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: hostMCPClientName, Version: hostMCPClientVersion},
		&sdkmcp.ClientOptions{Capabilities: capabilities},
	)
	if err := sdkmcp.AddSendingCustomMethod[*hostListSkillsParams, *hostListSkillsResult](client, "skills/list"); err != nil {
		t.Fatal(err)
	}
	if err := sdkmcp.AddSendingCustomMethod[*hostGetSkillParams, *hostGetSkillResult](client, "skills/get"); err != nil {
		t.Fatal(err)
	}
	return client
}

func knowlRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func hasHostTool(result *sdkmcp.ListToolsResult, name string) bool {
	for _, tool := range result.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func assertHostInvalidParams(t *testing.T, err error) {
	t.Helper()
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("error = %v, want invalid params", err)
	}
}
