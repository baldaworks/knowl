package hybrid

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func TestVectorCodecUsesPortableFloatOrderAndCosine(t *testing.T) {
	encoded, err := EncodeVector([]float32{1, 0}, 2)
	if err != nil || !bytes.Equal(encoded, []byte{0, 0, 128, 63, 0, 0, 0, 0}) {
		t.Fatalf("portable vector=%v %v", encoded, err)
	}
	got, err := DecodeVector(encoded, 2)
	if err != nil || !reflect.DeepEqual(got, []float32{1, 0}) {
		t.Fatalf("vector decode=%v %v", got, err)
	}
	for _, test := range []struct {
		right []float32
		want  float64
	}{{[]float32{1, 0}, 1}, {[]float32{0, 1}, 0}, {[]float32{-1, 0}, -1}} {
		score, err := Cosine(got, test.right)
		if err != nil || score != test.want {
			t.Fatalf("cosine=%v %v", score, err)
		}
	}
}
func TestVectorCodecRejectsWrongOrCorruptSpace(t *testing.T) {
	for _, vector := range [][]float32{{0, 0}, {1}, {2, 0}, {float32(math.Inf(1)), 0}, {float32(math.NaN()), 0}} {
		if _, err := EncodeVector(vector, 2); !errors.Is(err, app.ErrEmbedding) {
			t.Fatalf("invalid vector accepted: %v", err)
		}
	}
	for _, encoded := range [][]byte{{}, {0, 0, 0, 0, 0, 0, 0, 0}, {0, 0, 128, 127, 0, 0, 0, 0}} {
		if _, err := DecodeVector(encoded, 2); !errors.Is(err, app.ErrEmbedding) {
			t.Fatalf("corrupt stored vector accepted: %v", err)
		}
	}
}
