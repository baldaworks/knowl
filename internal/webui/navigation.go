package webui

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const trailParameter = "trail"

type navigationBudget struct {
	edges, bytes, windows int
	pages                 map[domain.PageID]domain.OperatorPage
}

func navigationPageURL(id domain.PageID, ancestors []domain.PageID) string {
	if len(ancestors) == 0 {
		if id == rootCatalogID {
			return knowledgePath
		}
		return pageURL(id)
	}
	encoded, _ := json.Marshal(ancestors)
	return knowledgeURL(url.Values{pageIDParameter: {string(id)}, parentIDParameter: {string(ancestors[len(ancestors)-1])}, trailParameter: {string(encoded)}})
}

func navigationAncestors(q url.Values, selected domain.PageID) ([]domain.PageID, bool) {
	limits := app.DefaultCatalogLimits()
	var ids []domain.PageID
	if q.Has(trailParameter) {
		raw := q.Get(trailParameter)
		if len(raw) > limits.MaxDepth*(limits.MaxPathBytes+4)+2 || json.Unmarshal([]byte(raw), &ids) != nil || len(ids) == 0 || len(ids) > limits.MaxDepth {
			return nil, false
		}
		if parent := q.Get(parentIDParameter); parent != "" && string(ids[len(ids)-1]) != parent {
			return nil, false
		}
	} else if parent := domain.PageID(q.Get(parentIDParameter)); parent != "" && parent != selected {
		ids = []domain.PageID{parent}
	}
	seen := map[domain.PageID]bool{selected: true}
	for _, id := range ids {
		kind, err := okf.ClassifyPath(string(id) + ".md")
		if seen[id] || len(id) > limits.MaxPathBytes || !validPageIdentity(string(id)) || !app.ValidOperatorReadPath(string(id)) || err != nil || kind != okf.DocumentIndex {
			return nil, false
		}
		seen[id] = true
	}
	return ids, true
}

func (h *Handler) readNavigationPage(ctx context.Context, id domain.PageID, budget *navigationBudget) (domain.OperatorPage, error) {
	if p, ok := budget.pages[id]; ok {
		return p, nil
	}
	limits := app.DefaultCatalogLimits()
	if err := ctx.Err(); err != nil {
		return domain.OperatorPage{}, err
	}
	if budget.windows >= limits.MaxEdges || budget.bytes >= limits.MaxSnapshotBytes {
		return domain.OperatorPage{}, app.ErrOperatorReadLimitExceeded
	}
	p, err := h.dependencies.Operator.Page(ctx, id)
	if err != nil {
		return p, err
	}
	budget.windows++
	budget.bytes += len(p.Markdown) + len(p.Title) + len(p.ID)
	if budget.bytes > limits.MaxSnapshotBytes {
		return domain.OperatorPage{}, app.ErrOperatorReadLimitExceeded
	}
	budget.pages[id] = p
	return p, nil
}

// Only links actually emitted by the safe renderer prove authored context.
// Bare index bullets, code, images and rejected URLs cannot become edges.
func navigationAuthoredEdge(parent domain.OperatorPage, child domain.PageID, budget *navigationBudget) (bool, error) {
	markup, err := renderTitledPageMarkdown(parent.Markdown, string(parent.ID), parent.Title)
	if err != nil {
		return false, err
	}
	budget.bytes += len(markup)
	limits := app.DefaultCatalogLimits()
	if budget.bytes > limits.MaxSnapshotBytes {
		return false, app.ErrOperatorReadLimitExceeded
	}
	nodes, err := html.ParseFragment(strings.NewReader(string(markup)), &html.Node{Type: html.ElementNode, Data: articleContainerElement, DataAtom: atom.Div})
	if err != nil {
		return false, err
	}
	found := false
	var visit func(*html.Node) error
	visit = func(n *html.Node) error {
		if n.DataAtom == atom.A {
			budget.edges++
			if budget.edges > limits.MaxEdges {
				return app.ErrOperatorReadLimitExceeded
			}
			for _, a := range n.Attr {
				if a.Key == linkHrefAttribute {
					u, err := url.Parse(a.Val)
					if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Path != knowledgePath {
						continue
					}
					target := u.Query().Get(pageIDParameter)
					if target == "" {
						target = u.Query().Get(parentIDParameter)
					}
					if target == string(child) {
						found = true
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := visit(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range nodes {
		if err := visit(n); err != nil {
			return false, err
		}
	}
	return found, nil
}

func (h *Handler) navigationEdge(ctx context.Context, parent domain.OperatorPage, child domain.PageID, budget *navigationBudget) (bool, error) {
	if ok, err := navigationAuthoredEdge(parent, child, budget); err != nil || ok {
		return ok, err
	}
	directory := path.Dir(string(parent.ID))
	if parent.ID == rootCatalogID {
		directory = ""
	}
	options := app.OperatorListOptions{Limit: 100}
	seen := map[string]bool{}
	limits := app.DefaultCatalogLimits()
	for {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if budget.windows >= limits.MaxEdges {
			return false, app.ErrOperatorReadLimitExceeded
		}
		branch, err := h.dependencies.Operator.WikiPageChildren(ctx, directory, options)
		if err != nil {
			return false, err
		}
		budget.windows++
		budget.edges += len(branch.Items)
		encoded, err := json.Marshal(branch)
		if err != nil {
			return false, err
		}
		budget.bytes += len(encoded)
		if budget.edges > limits.MaxEdges || budget.bytes > limits.MaxSnapshotBytes {
			return false, app.ErrOperatorReadLimitExceeded
		}
		for _, item := range branch.Items {
			if item.PageID == child {
				return true, nil
			}
		}
		if branch.NextCursor == "" || seen[branch.NextCursor] {
			return false, nil
		}
		seen[branch.NextCursor] = true
		options.Cursor = branch.NextCursor
	}
}

func (h *Handler) pageNavigation(ctx context.Context, q url.Values, page domain.OperatorPage) ([]knowledgeBreadcrumb, []domain.PageID) {
	if page.ID == rootCatalogID {
		return []knowledgeBreadcrumb{{Title: page.Title}}, nil
	}
	budget := &navigationBudget{bytes: len(page.Markdown) + len(page.Title) + len(page.ID), pages: map[domain.PageID]domain.OperatorPage{page.ID: page}}
	ids, valid := navigationAncestors(q, page.ID)
	parents := make([]domain.OperatorPage, 0, len(ids))
	if valid {
		for i, id := range ids {
			parent, err := h.readNavigationPage(ctx, id, budget)
			if err != nil {
				valid = false
				break
			}
			child := page.ID
			if i+1 < len(ids) {
				child = ids[i+1]
			}
			ok, err := h.navigationEdge(ctx, parent, child, budget)
			if err != nil || !ok {
				valid = false
				break
			}
			parents = append(parents, parent)
		}
	}
	if !valid {
		ids = nil
		parents = nil
	}
	crumbs := []knowledgeBreadcrumb{}
	if len(ids) == 0 || ids[0] != rootCatalogID {
		if root, err := h.readNavigationPage(ctx, rootCatalogID, budget); err == nil {
			crumbs = append(crumbs, knowledgeBreadcrumb{Title: root.Title, URL: knowledgePath})
		}
	}
	for i, parent := range parents {
		crumbs = append(crumbs, knowledgeBreadcrumb{Title: parent.Title, URL: navigationPageURL(parent.ID, ids[:i])})
	}
	crumbs = append(crumbs, knowledgeBreadcrumb{Title: page.Title})
	return crumbs, ids
}

// Carry verified visited IDs on ordinary authored links, including copy/open in
// another tab. The server validates them again on every resulting page read.
func navigationArticleLinks(markup string, current domain.PageID, ancestors []domain.PageID) (string, error) {
	kind, _ := okf.ClassifyPath(string(current) + ".md")
	if kind != okf.DocumentIndex {
		return markup, nil
	}
	nodes, err := html.ParseFragment(strings.NewReader(markup), &html.Node{Type: html.ElementNode, Data: articleContainerElement, DataAtom: atom.Div})
	if err != nil {
		return "", err
	}
	ids := append(append([]domain.PageID{}, ancestors...), current)
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.DataAtom == atom.A {
			for i, a := range n.Attr {
				if a.Key == linkHrefAttribute {
					u, err := url.Parse(a.Val)
					if err == nil && u.Path == knowledgePath && !u.IsAbs() && u.Host == "" && u.Query().Get(parentIDParameter) == string(current) && u.Query().Get(pageIDParameter) != "" {
						n.Attr[i].Val = navigationPageURL(domain.PageID(u.Query().Get(pageIDParameter)), ids)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	var result strings.Builder
	for _, n := range nodes {
		visit(n)
		if err := html.Render(&result, n); err != nil {
			return "", err
		}
	}
	return result.String(), nil
}
