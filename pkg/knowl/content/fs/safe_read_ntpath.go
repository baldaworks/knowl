package fs

import "strings"

// windowsReadRootName converts an absolute Win32 workspace root to the NT name
// accepted by descriptor-relative NtCreateFile reads.
func windowsReadRootName(path string) string {
	switch {
	case strings.HasPrefix(path, `\\?\`):
		return `\??\` + strings.TrimPrefix(path, `\\?\`)
	case strings.HasPrefix(path, `\\`):
		return `\??\UNC\` + strings.TrimPrefix(path, `\\`)
	default:
		return `\??\` + path
	}
}
