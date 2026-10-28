package searchtome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

var ErrPublication = errors.New("SearchTome corpus publication failed")

type PublishedCorpus struct {
	Snapshot knowl.WorkspaceSnapshot
	Titles   map[knowl.PageID]string
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Publish creates a canonical workspace in a fresh directory, using the public
// validated plan, staging, commit and snapshot interfaces.
func Publish(ctx context.Context, root, domain string, records []CorpusRecord) (PublishedCorpus, error) {
	if err := os.Mkdir(root, 0700); err != nil {
		return PublishedCorpus{}, ErrPublication
	}
	w, err := contentfs.New(root)
	if err != nil {
		return PublishedCorpus{}, err
	}
	if err = w.Init(); err != nil {
		return PublishedCorpus{}, err
	}
	scope := knowl.ScopeRef("searchtome-" + domain)
	schema, err := w.Schema(ctx, scope)
	if err != nil {
		return PublishedCorpus{}, err
	}
	rows := append([]CorpusRecord(nil), records...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Title < rows[j].Title })
	expected := map[knowl.PageID]string{}
	titles := map[knowl.PageID]string{}
	refs := map[knowl.PageID]string{}
	seen := map[string]bool{}
	index := []byte("---\nokf_version: \"0.2\"\n---\n# Knowl Index\n")
	for _, row := range rows {
		if row.Title == "" || row.Content == "" || seen[row.Title] {
			return PublishedCorpus{}, ErrPublication
		}
		seen[row.Title] = true
		hash := digest([]byte(domain + "\x00" + row.Title))
		id := knowl.PageID("concepts/" + hash)
		raw := []byte(row.Content)
		src, err := w.AcceptSource(ctx, knowl.SourceEnvelope{Scope: scope, Source: knowl.SourceRef{Adapter: "searchtome", ID: hash}, Version: knowl.SourceVersion{Version: "1", Digest: digest(raw)}, MediaType: "text/plain", Content: raw})
		if err != nil {
			return PublishedCorpus{}, fmt.Errorf("accept source: %w", err)
		}
		ref := app.SourceRefKey(src)
		meta, err := yaml.Marshal(map[string]any{"id": string(id), "title": row.Title, "type": "entity", "source_refs": []string{ref}})
		if err != nil {
			return PublishedCorpus{}, err
		}
		page := append([]byte("---\n"), meta...)
		page = append(page, []byte("---\n# "+row.Title+"\n\n"+row.Content)...)
		next := append(append([]byte{}, index...), []byte("\n* [Document]("+string(id)+".md)\n")...)
		edits := []knowl.FileEdit{{Path: "wiki/" + string(id) + ".md", Content: page}, {Path: "wiki/index.md", ExpectedDigest: digest(index), Content: next}}
		plan, err := app.ValidatePlan(ctx, knowl.MaintenanceInput{Scope: scope, Schema: schema, Source: src}, knowl.ModelEditPlan{SchemaDigest: schema.Digest, SourceRefs: []string{ref}, Edits: edits}, app.DefaultPlanLimits())
		if err != nil {
			return PublishedCorpus{}, fmt.Errorf("validate: %w", err)
		}
		staged, err := w.StagePlan(ctx, plan)
		if err != nil {
			return PublishedCorpus{}, fmt.Errorf("stage: %w", err)
		}
		if len(staged.Diagnostics) != 0 || len(staged.Files) != 2 {
			return PublishedCorpus{}, ErrPublication
		}
		if _, err = w.Commit(ctx, staged); err != nil {
			return PublishedCorpus{}, fmt.Errorf("commit: %w", err)
		}
		index = next
		expected[id] = string(page)
		titles[id] = row.Title
		refs[id] = ref
	}
	snapshot, err := w.Snapshot(ctx, scope)
	if err != nil {
		return PublishedCorpus{}, err
	}
	if len(snapshot.Pages) != len(rows) {
		return PublishedCorpus{}, ErrPublication
	}
	for _, page := range snapshot.Pages {
		want, ok := expected[page.ID]
		if !ok || page.Content != want || page.Title != titles[page.ID] || len(page.SourceRefs) != 1 || page.SourceRefs[0] != refs[page.ID] {
			return PublishedCorpus{}, ErrPublication
		}
	}
	return PublishedCorpus{Snapshot: snapshot, Titles: titles}, nil
}
