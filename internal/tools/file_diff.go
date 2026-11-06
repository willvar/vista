package tools

import (
	"strconv"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

type FileWrite struct {
	Path   string   `json:"path"`
	SHA256 string   `json:"sha256"`
	Diff   FileDiff `json:"diff"`
}

// FileDiff is the committed write's presentation artifact, not a rollback copy.
type FileDiff struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
	Unified string `json:"unified"`
	Omitted string `json:"omitted,omitempty"`
}

// Digest verification streams arbitrary-size originals. Keep a bounded copy
// from that same read, under the write lock, without limiting valid writes.
type diffCapture struct {
	text     strings.Builder
	tooLarge bool
}

func (c *diffCapture) Write(p []byte) (int, error) {
	if !c.tooLarge {
		if c.text.Len()+len(p) > maxFileBytes {
			c.tooLarge = true
			c.text.Reset()
		} else {
			_, _ = c.text.Write(p)
		}
	}
	return len(p), nil
}

func makeFileDiff(path string, created bool, before *diffCapture, content string) FileDiff {
	diff := FileDiff{Path: path, Created: created}
	if before.tooLarge {
		diff.Omitted = "too_large"
		return diff
	}
	oldLabel := strconv.Quote(path)
	if created {
		oldLabel = "/dev/null"
	}
	var err error
	diff.Unified, err = udiff.ToUnified(oldLabel, strconv.Quote(path), before.text.String(), udiff.Lines(before.text.String(), content), udiff.DefaultContextLines)
	if err != nil {
		diff.Omitted = "unavailable"
	}
	return diff
}
