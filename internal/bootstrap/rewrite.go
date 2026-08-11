package bootstrap

import (
	"path/filepath"
	"strings"
)

func rewriteObsidianReferences(document document, catalog catalog, body string) string {
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
			builder.WriteString(body[start:])
			break
		}
		raw := body[start+2 : start+2+end]
		builder.WriteString(rewriteObsidianReference(document.SourceRelative, catalog, raw, embed))
		offset = start + 2 + end + 2
	}
	return builder.String()
}

func rewriteObsidianReference(currentRelative string, catalog catalog, raw string, embed bool) string {
	targetPart := raw
	alias := ""
	if before, after, ok := strings.Cut(raw, "|"); ok {
		targetPart = before
		alias = after
	}
	target := targetPart
	anchor := ""
	if before, after, ok := strings.Cut(targetPart, "#"); ok {
		target = before
		anchor = after
	}
	target = strings.TrimSpace(target)
	if pageID, ok := resolvePageTarget(catalog, target); ok {
		var builder strings.Builder
		if embed {
			builder.WriteByte('!')
		}
		builder.WriteString("[[")
		builder.WriteString(pageID)
		if strings.TrimSpace(anchor) != "" {
			builder.WriteString("#")
			builder.WriteString(strings.TrimSpace(anchor))
		}
		if strings.TrimSpace(alias) != "" {
			builder.WriteString("|")
			builder.WriteString(strings.TrimSpace(alias))
		}
		builder.WriteString("]]")
		return builder.String()
	}
	if assetPath, ok := resolveAssetTarget(catalog, target); ok {
		relativeAssetPath := relativeAssetPath(currentRelative, assetPath)
		label := strings.TrimSpace(alias)
		if label == "" {
			label = filepath.Base(assetPath)
		}
		if embed {
			return "![](" + relativeAssetPath + ")"
		}
		return "[" + label + "](" + relativeAssetPath + ")"
	}
	if embed {
		return "![unsupported obsidian embed](" + strings.TrimSpace(raw) + ")"
	}
	return "[[" + raw + "]]"
}

func resolvePageTarget(catalog catalog, raw string) (string, bool) {
	key := referenceKey(strings.TrimSpace(raw))
	if key == "" {
		return "", false
	}
	if value, ok := catalog.NoteExact[key]; ok && value != "" {
		return value, true
	}
	base := strings.TrimSuffix(filepath.Base(key), filepath.Ext(key))
	if value, ok := catalog.NoteBasename[base]; ok && value != "" {
		return value, true
	}
	return "", false
}

func resolveAssetTarget(catalog catalog, raw string) (string, bool) {
	key := filepath.ToSlash(strings.TrimSpace(raw))
	key = strings.TrimPrefix(key, "./")
	if key == "" {
		return "", false
	}
	if value, ok := catalog.AssetExact[key]; ok && value != "" {
		return value, true
	}
	base := filepath.Base(key)
	if value, ok := catalog.AssetBasename[base]; ok && value != "" {
		return value, true
	}
	return "", false
}

func relativeAssetPath(currentRelative, assetTargetPath string) string {
	currentDir := filepath.Dir(filepath.ToSlash(filepath.Join("notes", currentRelative)))
	assetPath := strings.TrimPrefix(filepath.ToSlash(assetTargetPath), "wiki/")
	relative, err := filepath.Rel(filepath.FromSlash(currentDir), filepath.FromSlash(assetPath))
	if err != nil {
		return filepath.ToSlash(assetPath)
	}
	return filepath.ToSlash(relative)
}
