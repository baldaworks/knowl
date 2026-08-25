package okf

import (
	"path"
	"strings"
	"unicode/utf8"
)

const maxRelativePathBytes = 2048

const (
	indexFilename = "index.md"
	logFilename   = "log.md"
)

// ClassifyPath classifies a canonical bundle-relative path. Reserved names are
// recognized by basename at every directory depth.
func ClassifyPath(relative string) (DocumentKind, error) {
	if !validRelativePath(relative) {
		return "", violation(relative, RulePathInvalid)
	}

	switch path.Base(relative) {
	case indexFilename:
		return DocumentIndex, nil
	case logFilename:
		return DocumentLog, nil
	default:
		if path.Ext(relative) == ".md" {
			return DocumentConcept, nil
		}
		return DocumentAsset, nil
	}
}

func validRelativePath(relative string) bool {
	if relative == "" || relative == "." || len(relative) > maxRelativePathBytes ||
		!utf8.ValidString(relative) || strings.TrimSpace(relative) != relative ||
		strings.ContainsAny(relative, "\\\x00\r\n") || path.IsAbs(relative) ||
		path.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return false
	}
	for _, character := range relative {
		if character < ' ' || character == 0x7f {
			return false
		}
	}

	return true
}

func safeErrorPath(relative string) string {
	if validRelativePath(relative) {
		return relative
	}

	return "<invalid-path>"
}
