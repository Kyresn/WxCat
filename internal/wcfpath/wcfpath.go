// Package wcfpath converts Linux paths into the drive-letter form Wine exposes
// to Windows programs.
package wcfpath

import (
	"path/filepath"
	"strings"
)

// ToWine converts an absolute Linux path into a Z: drive path. Paths that are
// already Windows paths are returned unchanged.
func ToWine(path string) string {
	if path == "" {
		return ""
	}
	if len(path) >= 2 && path[1] == ':' {
		return path
	}
	clean := filepath.ToSlash(path)
	if !strings.HasPrefix(clean, "/") {
		return path
	}
	return "Z:" + strings.ReplaceAll(clean, "/", `\`)
}
