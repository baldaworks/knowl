package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	MaxPublicRetrievePages        = 3
	MaxPublicRetrieveSnippetRunes = 800
	MaxPublicRetrieveResultBytes  = 8 << 10
)

var (
	ErrPublicRetrieveResultTooLarge = errors.New("public retrieve result exceeds limit")
	ErrPublicRetrieveEncoding       = errors.New("public retrieve result encoding failed")
)

// PublicRetrieveRequest identifies one public search within an authorized scope.
type PublicRetrieveRequest struct {
	Scope   knowl.ScopeRef
	Query   string
	Sources []knowl.SourceID
}

// PublicEvidence is one compact, untrusted page match.
type PublicEvidence struct {
	PageID    knowl.PageID `json:"page_id"`
	Title     string       `json:"title"`
	Snippet   string       `json:"snippet"`
	Citation  *string      `json:"citation,omitempty"`
	Untrusted bool         `json:"untrusted"`
}

// PublicRetrieveResult is the shared bounded REST and MCP search result.
type PublicRetrieveResult struct {
	Retrieval *knowl.RetrievalStatus `json:"retrieval,omitempty"`
	Query     string                 `json:"query"`
	Evidence  []PublicEvidence       `json:"evidence"`
}

// RetrievePublic replaces the public rich query result with compact evidence cards.
// Internal callers that require complete retrieval context continue to use Query.
func (service *QueryService) RetrievePublic(ctx context.Context, request PublicRetrieveRequest) (PublicRetrieveResult, error) {
	limits := service.limits
	limits.Pages = min(limits.Pages, MaxPublicRetrievePages)
	limits.Characters = min(limits.Characters, MaxPublicRetrieveSnippetRunes)
	pages, report, err := service.searchWithReport(ctx, request.Scope, request.Query, limits, request.Sources)
	if err != nil {
		return PublicRetrieveResult{Retrieval: PublicRetrievalStatus(report)}, err
	}
	result := PublicRetrieveResult{
		Retrieval: PublicRetrievalStatus(report),
		Query:     strings.TrimSpace(request.Query),
		Evidence:  make([]PublicEvidence, 0, len(pages)),
	}
	for _, page := range pages {
		if !utf8.ValidString(string(page.ID)) || !utf8.ValidString(page.Title) || !utf8.ValidString(page.Snippet) {
			return PublicRetrieveResult{}, ErrPublicRetrieveEncoding
		}
		result.Evidence = append(result.Evidence, PublicEvidence{
			PageID: page.ID, Title: page.Title,
			Snippet:  truncateRunes(page.Snippet, MaxPublicRetrieveSnippetRunes),
			Citation: publicEvidenceCitation(page), Untrusted: true,
		})
	}
	return fitPublicRetrieveResult(result)
}

func publicEvidenceCitation(page knowl.PageReference) *string {
	refs := make([]string, 0, len(page.SourceRefs))
	for _, ref := range page.SourceRefs {
		ref = strings.TrimSpace(ref)
		if ref != "" && utf8.ValidString(ref) {
			refs = append(refs, ref)
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return &refs[0]
	}
	if id := strings.TrimSpace(string(page.ID)); id != "" {
		return &id
	}
	return nil
}

func fitPublicRetrieveResult(result PublicRetrieveResult) (PublicRetrieveResult, error) {
	for {
		encoded, err := json.Marshal(result)
		if err != nil {
			return PublicRetrieveResult{}, fmt.Errorf("encode public retrieve result: %w: %w", err, ErrPublicRetrieveEncoding)
		}
		if len(encoded) <= MaxPublicRetrieveResultBytes {
			return result, nil
		}
		if len(result.Evidence) <= 1 {
			return PublicRetrieveResult{}, fmt.Errorf("public retrieve result bytes %d exceed %d: %w", len(encoded), MaxPublicRetrieveResultBytes, ErrPublicRetrieveResultTooLarge)
		}
		result.Evidence = result.Evidence[:len(result.Evidence)-1]
	}
}
