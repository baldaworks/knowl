package hybrid

import (
	"encoding/binary"
	"math"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// EncodeVector persists finite nonzero normalized float32 with fixed byte order.
func EncodeVector(vector []float32, dimensions int) ([]byte, error) {
	if err := validateVector(vector, dimensions); err != nil {
		return nil, err
	}
	encoded := make([]byte, dimensions*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(encoded[i*4:], math.Float32bits(value))
	}
	return encoded, nil
}

// DecodeVector fails closed rather than skipping malformed/corrupt stored rows.
func DecodeVector(encoded []byte, dimensions int) ([]float32, error) {
	if dimensions < 1 || dimensions > 4096 || len(encoded) != dimensions*4 {
		return nil, failure(knowl.RetrievalProjectionDrift)
	}
	vector := make([]float32, dimensions)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[i*4:]))
	}
	if err := validateVector(vector, dimensions); err != nil {
		return nil, err
	}
	return vector, nil
}
func validateVector(vector []float32, dimensions int) error {
	if dimensions < 1 || dimensions > 4096 || len(vector) != dimensions {
		return failure(knowl.RetrievalDimensionMismatch)
	}
	squared := 0.0
	for _, value := range vector {
		v := float64(value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return failure(knowl.RetrievalProjectionDrift)
		}
		squared += v * v
	}
	if math.Abs(squared-1) > 0.001 {
		return failure(knowl.RetrievalProjectionDrift)
	}
	return nil
}

// Cosine compares validated unit vectors using stable float64 accumulation.
func Cosine(left, right []float32) (float64, error) {
	if err := validateVector(left, len(left)); err != nil {
		return 0, err
	}
	if err := validateVector(right, len(left)); err != nil {
		return 0, err
	}
	dot := 0.0
	for i, v := range left {
		dot += float64(v) * float64(right[i])
	}
	return max(-1, min(1, dot)), nil
}
