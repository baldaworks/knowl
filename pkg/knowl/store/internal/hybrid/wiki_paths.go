package hybrid

import (
	"sort"
	"strings"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/text/unicode/norm"
)

// NormalizeWikiPaths gives stored and freshly captured paths the same semantic
// identity without changing source page bytes or evidence windows.
func NormalizeWikiPaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	total := 0
	for _, path := range paths {
		if !utf8.ValidString(path) {
			return nil, failure(knowl.RetrievalInvalidInput)
		}
		total += len(path)
		if total > MaxSemanticBytes {
			return nil, failure(knowl.RetrievalInputLimit)
		}
		value := norm.NFC.String(strings.Join(strings.Fields(path), " "))
		if value != "" {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	unique := result[:0]
	for _, path := range result {
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	return unique, nil
}
