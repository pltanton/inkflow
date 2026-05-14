package util

import (
	"path"
	"path/filepath"
	"strings"
)

func SlashPath(parts ...string) string {
	return path.Clean(path.Join(parts...))
}

func VaultLink(p string) string {
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	return strings.TrimSuffix(p, ".md")
}
