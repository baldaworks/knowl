package mcpsdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	skillsExtensionID = "io.modelcontextprotocol/skills"
	skillsListMethod  = "skills/list"
	skillsGetMethod   = "skills/get"
	publicCacheScope  = "public"
)

type listSkillsParams struct {
	sdkmcp.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}

type getSkillParams struct {
	sdkmcp.ParamsBase
	URI string `json:"uri"`
}

type skillManifestResource struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

type skillEntry struct {
	URI         string                  `json:"uri"`
	Frontmatter map[string]any          `json:"frontmatter"`
	Resources   []skillManifestResource `json:"resources"`
}

type listSkillsResult struct {
	sdkmcp.ResultBase
	sdkmcp.Cacheable
	ResultType string       `json:"resultType"`
	Skills     []skillEntry `json:"skills"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

type getSkillResult struct {
	sdkmcp.ResultBase
	sdkmcp.Cacheable
	ResultType string     `json:"resultType"`
	Skill      skillEntry `json:"skill"`
}

func attachSkillsExtension(server *sdkmcp.Server, catalog skillCatalog) error {
	if server == nil {
		return fmt.Errorf("MCP server is required")
	}
	if err := validateSkillCatalogForExtension(catalog); err != nil {
		return err
	}
	entry := skillEntryFromCatalog(catalog)

	if err := sdkmcp.AddReceivingCustomMethod(server, skillsListMethod,
		func(_ context.Context, _ *sdkmcp.ServerSession, params *listSkillsParams) (*listSkillsResult, error) {
			if params != nil && params.Cursor != "" {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Invalid cursor"}
			}
			return &listSkillsResult{
				Cacheable:  sdkmcp.Cacheable{CacheScope: publicCacheScope},
				ResultType: "complete",
				Skills:     []skillEntry{entry},
			}, nil
		}); err != nil {
		return fmt.Errorf("register %s: %w", skillsListMethod, err)
	}
	if err := sdkmcp.AddReceivingCustomMethod(server, skillsGetMethod,
		func(_ context.Context, _ *sdkmcp.ServerSession, params *getSkillParams) (*getSkillResult, error) {
			if params == nil || params.URI != catalog.URI {
				uri := ""
				if params != nil {
					uri = params.URI
				}
				return nil, sdkmcp.ResourceNotFoundError(uri)
			}
			return &getSkillResult{
				Cacheable:  sdkmcp.Cacheable{CacheScope: publicCacheScope},
				ResultType: "complete",
				Skill:      entry,
			}, nil
		}); err != nil {
		return fmt.Errorf("register %s: %w", skillsGetMethod, err)
	}

	server.AddResource(&sdkmcp.Resource{
		URI:         catalog.Resource.URI,
		Name:        catalog.Frontmatter["name"].(string),
		Description: catalog.Frontmatter["description"].(string),
		MIMEType:    catalog.Resource.MediaType,
		Size:        catalog.Resource.Size,
	}, func(_ context.Context, request *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		if request == nil || request.Params == nil || request.Params.URI != catalog.Resource.URI {
			uri := ""
			if request != nil && request.Params != nil {
				uri = request.Params.URI
			}
			return nil, sdkmcp.ResourceNotFoundError(uri)
		}
		return &sdkmcp.ReadResourceResult{
			Cacheable: sdkmcp.Cacheable{CacheScope: publicCacheScope},
			Contents: []*sdkmcp.ResourceContents{{
				URI:      catalog.Resource.URI,
				MIMEType: catalog.Resource.MediaType,
				Text:     string(catalog.Resource.Content),
			}},
		}, nil
	})
	return nil
}

func validateSkillCatalogForExtension(catalog skillCatalog) error {
	parsed, err := url.Parse(catalog.URI)
	if err != nil || parsed.Scheme == "" || catalog.URI != knowlSkillURI || catalog.URI != catalog.Resource.URI {
		return fmt.Errorf("invalid Knowl skill URI")
	}
	name, nameOK := catalog.Frontmatter["name"].(string)
	description, descriptionOK := catalog.Frontmatter["description"].(string)
	if !nameOK || name == "" || !descriptionOK || description == "" {
		return fmt.Errorf("invalid Knowl skill frontmatter")
	}
	digest := sha256.Sum256(catalog.Resource.Content)
	if catalog.Resource.MediaType != markdownMediaType ||
		catalog.Resource.Size != int64(len(catalog.Resource.Content)) ||
		catalog.Resource.SHA256 != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("invalid Knowl skill resource metadata")
	}
	return nil
}

func skillEntryFromCatalog(catalog skillCatalog) skillEntry {
	return skillEntry{
		URI:         catalog.URI,
		Frontmatter: catalog.Frontmatter,
		Resources: []skillManifestResource{{
			URI:    catalog.Resource.URI,
			Digest: "sha256:" + catalog.Resource.SHA256,
			Size:   catalog.Resource.Size,
		}},
	}
}
