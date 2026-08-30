package obsidian

import (
	"strings"

	"personal-mcp-gateway/internal/fsx"
)

// pathCoordinate projects canonical vault identities into the coordinate
// system selected by one request's explicit base. It is presentation-only:
// filesystem access, ordering, fingerprints, and cursors continue to use the
// canonical vault-relative identity.
type pathCoordinate struct {
	base     string
	segments []string
}

func newPathCoordinate(base string) (pathCoordinate, error) {
	if strings.TrimSpace(base) == "" {
		base = "."
	}
	normalized, err := fsx.NormalizePath("", base)
	if err != nil {
		return pathCoordinate{}, err
	}
	return normalizedPathCoordinate(normalized), nil
}

func normalizedPathCoordinate(normalized string) pathCoordinate {
	coordinate := pathCoordinate{base: normalized}
	if normalized != "." {
		coordinate.segments = strings.Split(normalized, "/")
	}
	return coordinate
}

func (c pathCoordinate) project(canonical string) string {
	if c.base == "." {
		return canonical
	}
	if canonical == c.base {
		return "."
	}
	if prefix := c.base + "/"; strings.HasPrefix(canonical, prefix) {
		return canonical[len(prefix):]
	}
	target := []string(nil)
	if canonical != "." {
		target = strings.Split(canonical, "/")
	}
	common := 0
	for common < len(c.segments) && common < len(target) && c.segments[common] == target[common] {
		common++
	}
	parts := make([]string, 0, len(c.segments)-common+len(target)-common)
	for range c.segments[common:] {
		parts = append(parts, "..")
	}
	parts = append(parts, target[common:]...)
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts, "/")
}

func projectChild(parent, name string) string {
	if parent == "." {
		return name
	}
	return parent + "/" + name
}
