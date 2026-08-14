package app

import "strings"

const maxSourceTitleRunes = 256

func sourceTitle(content []byte) string {
	var firstNonEmpty string
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if firstNonEmpty == "" {
			firstNonEmpty = line
		}
		if heading, ok := atxHeading(line); ok {
			return truncateSourceTitle(heading)
		}
	}
	return truncateSourceTitle(firstNonEmpty)
}

func atxHeading(line string) (string, bool) {
	markers := 0
	for markers < len(line) && markers < 6 && line[markers] == '#' {
		markers++
	}
	if markers == 0 || markers >= len(line) || (line[markers] != ' ' && line[markers] != '\t') {
		return "", false
	}
	heading := strings.TrimSpace(line[markers:])
	return heading, heading != ""
}

func truncateSourceTitle(title string) string {
	characters := []rune(strings.TrimSpace(title))
	if len(characters) > maxSourceTitleRunes {
		characters = characters[:maxSourceTitleRunes]
	}
	return string(characters)
}
