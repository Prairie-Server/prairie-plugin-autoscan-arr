package arr

import (
	"strings"

	"github.com/Silo-Server/silo-plugin-autoscan-arr/internal/config"
)

// NormalizeSeparators converts Windows backslash separators to forward slashes
// so paths from a Windows-hosted arr resolve on the Linux host. (filepath.ToSlash
// is a no-op on Linux, so the replacement is explicit.)
func NormalizeSeparators(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

// ApplyRewrites returns path with the first matching prefix rewrite applied,
// or path unchanged when none match. Matching is boundary-safe: From matches
// only at a path-segment boundary (exact, or prefix followed by '/'), so
// From="/data/media" does not rewrite "/data/media2/x".
func ApplyRewrites(path string, rewrites []config.Rewrite) string {
	for _, rw := range rewrites {
		from := strings.TrimSpace(rw.From)
		if from == "" {
			continue
		}
		trimmed := strings.TrimSuffix(from, "/")
		if path == trimmed || strings.HasPrefix(path, trimmed+"/") {
			return strings.TrimSpace(rw.To) + strings.TrimPrefix(path, trimmed)
		}
	}
	return path
}
