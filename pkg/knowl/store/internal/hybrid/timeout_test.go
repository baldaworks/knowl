package hybrid

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type deadlineProvider struct {
	deadline time.Time
	block    bool
}

func (p *deadlineProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	p.deadline, _ = ctx.Deadline()
	if p.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestDenseBuildOperationalDeadlineAndIdentity(t *testing.T) {
	p := &deadlineProvider{}
	options := app.EmbeddingOptions{Provider: p, Space: app.EmbeddingSpace{Model: "budget-test", Revision: "1", Dimensions: 2}}
	base, err := New([]app.EmbeddingOptions{options})
	if err != nil {
		t.Fatal(err)
	}
	options.RebuildTimeout = time.Hour
	engine, err := New([]app.EmbeddingOptions{options})
	if err != nil {
		t.Fatal(err)
	}
	if engine.Fingerprint != base.Fingerprint || *engine.MaintenancePolicy() != *base.MaintenancePolicy() {
		t.Fatal("runtime timeout changed immutable embedding/maintenance identity")
	}
	page := knowl.PageSnapshot{ID: "budget", Path: "wiki/budget.md", Digest: strings.Repeat("a", 64), Title: "Budget", Body: "Deadline evidence"}
	snapshot := knowl.WorkspaceSnapshot{Pages: []knowl.PageSnapshot{page}}
	if _, _, err := engine.Build(context.Background(), snapshot, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(p.deadline); remaining < 59*time.Minute || remaining > time.Hour {
		t.Fatalf("build budget=%v", remaining)
	}
	parent, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := parent.Deadline()
	if _, _, err := engine.Build(parent, snapshot, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if !p.deadline.Equal(deadline) {
		t.Fatal("build extended the caller deadline")
	}
	options.RebuildTimeout = 20 * time.Millisecond
	p.block = true
	engine, err = New([]app.EmbeddingOptions{options})
	if err != nil {
		t.Fatal(err)
	}
	if _, chunks, err := engine.Build(t.Context(), snapshot, strings.Repeat("b", 64)); !errors.Is(err, context.DeadlineExceeded) || len(chunks) != 0 {
		t.Fatalf("timeout returned chunks=%d err=%v", len(chunks), err)
	}
	options.RebuildTimeout = -time.Second
	if _, err := New([]app.EmbeddingOptions{options}); !errors.Is(err, app.ErrEmbedding) {
		t.Fatalf("negative budget accepted: %v", err)
	}
}
