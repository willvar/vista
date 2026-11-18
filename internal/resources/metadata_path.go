package resources

import (
	"path/filepath"
	"runtime"
	"strings"
)

// DOFS v0.1.4 formats an absolute Windows drive path as file://C:/..., which
// SQLite rejects as an authority. The native extended-length path keeps an
// empty URI authority without replacing DOFS's metadata or durability logic.
func metadataPath(stateDir, id string) string {
	path := filepath.Join(stateDir, "resource-backends", id, "metadata.sqlite")
	if runtime.GOOS == "windows" && !strings.HasPrefix(path, `\\`) {
		if absolute, err := filepath.Abs(path); err == nil {
			return `\\?\` + absolute
		}
	}
	return path
}
