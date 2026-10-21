package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputBlocksPreserveUnicodeAndLiveTail(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "outputs", "job.log")
	app := &App{dir: root}
	type page struct {
		Content                            string
		Offset, NextOffset, AvailableBytes int64
		Complete                           bool
	}
	read := func(offset, limit int, complete bool) page {
		t.Helper()
		response := httptest.NewRecorder()
		app.outputPage(response, httptest.NewRequest("GET", fmt.Sprintf("/output?offset=%d&limit=%d", offset, limit), nil), "job", path, complete)
		var got page
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &got) != nil || !utf8.ValidString(got.Content) || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("range %d: HTTP %d %s", offset, response.Code, response.Body.String())
		}
		if len(got.Content) > limit+3 {
			t.Fatal("unbounded range")
		}
		return got
	}
	content := strings.Repeat("中文🙂café\n", 10000)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{4, 7, 16384, 65536} {
		var got strings.Builder
		for offset := 0; offset < len(content); offset += limit {
			got.WriteString(read(offset, limit, true).Content)
		}
		if got.String() != content {
			t.Fatalf("block size %d lost or duplicated Unicode: bytes %d != %d", limit, got.Len(), len(content))
		}
	}
	// The tail rune crosses the fixed block boundary and is still being written.
	content = strings.Repeat("x", 16383) + "🙂"
	if err := os.WriteFile(path, []byte(content[:16385]), 0600); err != nil {
		t.Fatal(err)
	}
	if got := read(0, 16384, false); got.Content != content[:16383] || got.NextOffset != 16383 || got.AvailableBytes != 16385 {
		t.Fatalf("incomplete rune: %+v", got)
	}
	if got := read(16384, 16384, false); got.Content != "" {
		t.Fatalf("continuation bytes leaked: %+v", got)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if got := read(0, 16384, false).Content + read(16384, 16384, false).Content; got != content {
		t.Fatal("completed tail rune lost")
	}
	for _, query := range []string{"limit=0", "limit=-1", "limit=65537", "limit=x", "limit=4&full=1", "limit=4&offset=-1"} {
		response := httptest.NewRecorder()
		app.outputPage(response, httptest.NewRequest("GET", "/output?"+query, nil), "job", path, false)
		if response.Code != 400 {
			t.Fatalf("invalid range %s: %d", query, response.Code)
		}
	}
}
