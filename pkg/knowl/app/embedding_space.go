package app

import (
	"strings"
	"unicode"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// NormalizeEmbeddingSpace validates shared nonsecret model/preprocessing input.
func NormalizeEmbeddingSpace(space EmbeddingSpace) (EmbeddingSpace, error) {
	space.Model = strings.TrimSpace(space.Model)
	space.Revision = strings.TrimSpace(space.Revision)
	if !validEmbeddingIdentity(space.Model) || !validEmbeddingIdentity(space.Revision) || space.Dimensions < 1 || space.Dimensions > 4096 || !validEmbeddingPrefix(space.QueryPrefix) || !validEmbeddingPrefix(space.PassagePrefix) {
		return EmbeddingSpace{}, &EmbeddingError{Code: knowl.RetrievalInvalidConfiguration}
	}
	return space, nil
}
func validEmbeddingIdentity(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validEmbeddingPrefix(value string) bool {
	return len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
