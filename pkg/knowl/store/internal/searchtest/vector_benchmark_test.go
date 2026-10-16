package searchtest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
)

func TestVectorBenchmarkFixtureReproducibleAndExactlyCounted(t *testing.T) {
	for _, windows := range []int{1, 4} {
		c := VectorBenchmarkCase{128, 8, windows, 50}
		first, options, query, sources, checksum, err := VectorRuntimeFixture(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
		second, _, _, _, checksum2, err := VectorRuntimeFixture(t.Context(), c)
		if err != nil || checksum != checksum2 || !reflect.DeepEqual(first, second) || len(sources) != 1 {
			t.Fatalf("reproduction checksum=%s/%s sources=%v err=%v", checksum, checksum2, sources, err)
		}
		provider := options.Provider.(*VectorBenchmarkProvider)
		if len(provider.Chunks) != c.Chunks || len(provider.Queries) != windows {
			t.Fatalf("chunks=%d windows=%d", len(provider.Chunks), len(provider.Queries))
		}
		for _, chunk := range provider.Chunks {
			if _, err := hybrid.EncodeVector(chunk.Vector, c.Dimensions); err != nil {
				t.Fatal(err)
			}
		}
		prepared, err := hybrid.PrepareQuery(t.Context(), query, options.Space)
		if err != nil {
			t.Fatal(err)
		}
		vectors, err := provider.Embed(t.Context(), prepared.Inputs)
		if err != nil {
			t.Fatal(err)
		}
		original := provider.Vectors[prepared.Inputs[0]][0]
		vectors[0][0] = 42
		if provider.Vectors[prepared.Inputs[0]][0] != original {
			t.Fatal("provider exposed mutable fixture")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, _, _, err := VectorRuntimeFixture(ctx, VectorBenchmarkCase{128, 8, 1, 50}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestVectorBenchmarkDefaultAndResourceBounds(t *testing.T) {
	t.Setenv("KNOWL_VECTOR_BENCH_LAB", "")
	if got := VectorBenchmarkCases(true); len(got) != 2 || got[0].Chunks != 128 || got[1].Windows != 4 {
		t.Fatalf("defaults=%v", got)
	}
	if VectorBenchmarkResourceEstimate(VectorBenchmarkCase{100000, 768, 4, 50}) <= 512<<20 {
		t.Fatal("large case must not allocate under lab budget")
	}
	if VectorBenchmarkResourceEstimate(VectorBenchmarkCase{100000, 2, 4, 50}) > 512<<20 {
		t.Fatal("bounded low-dimension kernel should fit estimate")
	}
}
