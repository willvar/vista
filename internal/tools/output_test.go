package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputPagesReadLargeArtifactWithoutLosingUTF8(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "outputs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("证据🙂\n", 120000)
	path := filepath.Join(dir, "large.log")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	var recovered strings.Builder
	for offset := int64(0); offset < int64(len(content)); {
		page, err := ReadOutputPage(root, path, offset, MaxOutputPageBytes)
		if err != nil || page.NextOffset <= offset || !utf8.ValidString(page.Content) || page.AvailableBytes != int64(len(content)) {
			t.Fatalf("page at %d: %+v, %v", offset, page, err)
		}
		if page.Truncated != (page.NextOffset < page.AvailableBytes) {
			t.Fatal("incorrect truncation metadata")
		}
		recovered.WriteString(page.Content)
		offset = page.NextOffset
	}
	if recovered.String() != content {
		t.Fatal("pagination lost or duplicated output")
	}
	for _, input := range []struct {
		offset int64
		limit  int
	}{{-1, 0}, {int64(len(content) + 1), 0}, {0, -1}, {0, 3}, {0, MaxOutputPageBytes + 1}} {
		if _, err := ReadOutputPage(root, path, input.offset, input.limit); err == nil {
			t.Fatalf("accepted invalid range: %+v", input)
		}
	}
}

func TestOutputReaderStaysInsideOutputDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "private.txt")
	if err := os.WriteFile(outside, []byte("outside output storage"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOutputPage(root, outside, 0, 0); err == nil {
		t.Fatal("read escaped output directory")
	}
	if _, err := ReadOutputPreview(root, outside, 0); err == nil {
		t.Fatal("preview escaped output directory")
	}
	link := filepath.Join(root, "outputs", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadOutputPage(root, link, 0, 0); err == nil {
		t.Fatal("read followed symlink outside output directory")
	}
	if _, err := ReadOutputPreview(root, link, 0); err == nil {
		t.Fatal("preview followed symlink outside output directory")
	}
}
