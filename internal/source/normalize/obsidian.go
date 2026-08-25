package normalize

import (
	"net/url"
	"path"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func rewriteObsidianReferences(sourceID knowl.SourceID, currentDocument string, catalog Catalog, body string) string {
	var builder strings.Builder
	for offset := 0; offset < len(body); {
		start := strings.Index(body[offset:], "[[")
		if start < 0 {
			builder.WriteString(body[offset:])
			break
		}
		start += offset
		embed := start > 0 && body[start-1] == '!'
		prefixEnd := start
		if embed {
			prefixEnd--
		}
		builder.WriteString(body[offset:prefixEnd])
		end := strings.Index(body[start+2:], "]]")
		if end < 0 {
			builder.WriteString(body[prefixEnd:])
			break
		}
		raw := body[start+2 : start+2+end]
		builder.WriteString(rewriteObsidianReference(sourceID, currentDocument, catalog, raw, embed))
		offset = start + 2 + end + 2
	}
	return builder.String()
}

func rewriteObsidianReference(sourceID knowl.SourceID, currentDocument string, catalog Catalog, raw string, embed bool) string {
	targetPart, alias, _ := strings.Cut(raw, "|")
	target, anchor, _ := strings.Cut(targetPart, "#")
	target = strings.TrimSpace(target)
	alias = strings.TrimSpace(alias)
	anchor = strings.TrimSpace(anchor)
	if pageID, ok := catalog.ResolveNote(target); ok {
		return renderNoteReference(sourceID, pageID, alias, anchor, embed)
	}
	if assetPath, ok := catalog.ResolveAsset(target); ok {
		return renderAssetReference(sourceID, currentDocument, assetPath, alias, embed)
	}
	if embed {
		return "![unsupported obsidian embed](" + strings.TrimSpace(raw) + ")"
	}
	return "[[" + raw + "]]"
}

func renderNoteReference(sourceID knowl.SourceID, pageID, alias, anchor string, embed bool) string {
	var builder strings.Builder
	if embed {
		builder.WriteByte('!')
	}
	builder.WriteString("[[sources/")
	builder.WriteString(string(sourceID))
	builder.WriteByte('/')
	builder.WriteString(pageID)
	if anchor != "" {
		builder.WriteByte('#')
		builder.WriteString(anchor)
	}
	if alias != "" {
		builder.WriteByte('|')
		builder.WriteString(alias)
	}
	builder.WriteString("]]")
	return builder.String()
}

func renderAssetReference(sourceID knowl.SourceID, currentDocument, assetPath, alias string, embed bool) string {
	relative := escapeRelativePath(relativeAssetPath(sourceID, currentDocument, assetPath))
	label := alias
	if label == "" {
		label = path.Base(assetPath)
	}
	label = strings.NewReplacer("\\", "\\\\", "]", "\\]").Replace(label)
	if embed {
		return "![](" + relative + ")"
	}
	return "[" + label + "](" + relative + ")"
}

func relativeAssetPath(sourceID knowl.SourceID, currentDocument, assetPath string) string {
	currentDir := path.Dir("sources/" + string(sourceID) + "/" + currentDocument)
	target := "sources/" + string(sourceID) + "/" + assetPath
	currentParts := strings.Split(currentDir, "/")
	targetParts := strings.Split(target, "/")
	common := 0
	for common < len(currentParts) && common < len(targetParts) && currentParts[common] == targetParts[common] {
		common++
	}
	parts := make([]string, 0, len(currentParts)-common+len(targetParts)-common)
	for index := common; index < len(currentParts); index++ {
		parts = append(parts, "..")
	}
	parts = append(parts, targetParts[common:]...)
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts, "/")
}

func escapeRelativePath(relative string) string {
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		if part == "." || part == ".." {
			continue
		}
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
