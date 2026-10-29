package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestWebfetchPreservesPageAndReadsSavedRangesWithoutRefetching(t *testing.T) {
	large := strings.Repeat("你好🙂 evidence ", 15000) + "FINAL EVIDENCE"
	for _, tc := range []struct{ name, contentType, format, body, want string }{
		{"plain text", "text/plain", "text", large, large},
		{"reader Markdown", "text/markdown; charset=utf-8", "markdown", large, large},
		{"JSON escaping", "text/plain", "text", `{"data":"` + strings.Repeat(`<🙂\n\"`, 24000) + `"}`, `{"data":"` + strings.Repeat(`<🙂\n\"`, 24000) + `"}`},
		{"line budget", "text/plain", "text", strings.Repeat("evidence\n", 2500), strings.Repeat("evidence\n", 2500)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, save := outputToolStore(t)
			var artifactEvents int
			emit := func(event workerproto.Result) error {
				if event.Artifact != nil {
					artifactEvents++
				}
				return save(event)
			}
			var downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			routeReaderFixture(t, server.URL)
			call := provider.ToolCall{ID: "fetch-page"}
			call.Function.Name = "webfetch"
			call.Function.Arguments = fmt.Sprintf(`{"url":%q,"format":%q}`, "https://93.184.216.34/fixture", tc.format)
			parent := workerproto.Command{SessionID: "session", RunID: "run", TaskID: "main", Webfetch: workerproto.WebfetchConfig{PreviewBytes: 1000}}
			runtime := &toolRuntime{parent: parent, emit: emit}
			ctx := context.Background()
			output, err := runtime.execute(ctx, call)
			var preview webfetchResult
			if err != nil || json.Unmarshal([]byte(output), &preview) != nil {
				t.Fatalf("fetch result: %v", err)
			}
			if preview.ArtifactID == "" || !preview.Complete || !preview.Truncated || preview.Bytes != int64(len(tc.want)) || !utf8.ValidString(preview.Content) || !strings.HasPrefix(tc.want, preview.Content) || preview.NextOffset != len(preview.Content) || exceedsToolBudget(output) || !strings.Contains(preview.Hint, "read_tool_output") {
				t.Fatalf("invalid saved page preview: bytes=%d, next=%d, artifact=%q, complete=%t", preview.Bytes, preview.NextOffset, preview.ArtifactID, preview.Complete)
			}
			artifact, err := db.GetArtifact(ctx, "session", preview.ArtifactID)
			if err != nil || !artifact.Complete || artifact.Bytes != int64(len(tc.want)) {
				t.Fatalf("saved artifact: %+v, %v", artifact, err)
			}
			full, err := os.ReadFile(artifact.OutputPath)
			if err != nil || string(full) != tc.want {
				t.Fatalf("saved text differs: %d bytes, %v", len(full), err)
			}
			bounded, err := boundToolOutput(ctx, "run", call.ID, call.Function.Name, output, resultOutputRef(output), emit)
			if err != nil || bounded.reference == nil || bounded.reference.ArtifactID != preview.ArtifactID || artifactEvents != 2 {
				t.Fatalf("tool pipeline lost or duplicated the artifact: events=%d, %v", artifactEvents, err)
			}
			// The page is gone; continuation must read the saved decoded text.
			server.Close()
			var got strings.Builder
			got.WriteString(preview.Content)
			for offset := int64(preview.NextOffset); offset < int64(len(tc.want)); {
				arguments := fmt.Sprintf(`{"callId":%q,"offset":%d,"limit":32768}`, call.ID, offset)
				output, err := readToolOutput(ctx, arguments, parent)
				var page tools.OutputPage
				if err != nil || json.Unmarshal([]byte(output), &page) != nil || page.NextOffset <= offset || exceedsToolBudget(output) {
					t.Fatalf("saved range at %d: %v", offset, err)
				}
				got.WriteString(page.Content)
				offset = page.NextOffset
			}
			if got.String() != tc.want || downloads.Load() != 1 {
				t.Fatalf("page continuation differs or refetched: bytes=%d, downloads=%d", got.Len(), downloads.Load())
			}
			// Direct access to the tail needs no preceding page reads.
			tail := "FINAL EVIDENCE"
			if strings.HasSuffix(tc.want, tail) {
				output, err := readToolOutput(ctx, fmt.Sprintf(`{"artifactId":%q,"offset":%d}`, preview.ArtifactID, len(tc.want)-len(tail)), parent)
				var page tools.OutputPage
				if err != nil || json.Unmarshal([]byte(output), &page) != nil || page.Content != tail || page.Truncated {
					t.Fatalf("random range lost page tail: %v", err)
				}
			}
		})
	}
}

func TestWebfetchInitialBytesKeepFullPageReadable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limit   int
		content string
	}{
		{"Unicode", 3, "中文🙂and the remaining evidence"},
		{"partial emoji", 3, "🙂evidence"},
		{"one emoji", 4, "🙂evidence"},
		{"default bytes", 50 << 10, strings.Repeat("中文🙂", 6000)},
		{"larger than old byte preview", 10000, strings.Repeat("evidence", 2000)},
		{"larger than fixed byte budgets", 90000, strings.Repeat("证据🙂evidence", 15000)},
		{"larger than fixed line budget", 5000, strings.Repeat("evidence\n", 10000)},
		{"escaped content", 20000, strings.Repeat("<\x00🙂\\\"", 5000)},
		{"unlimited complete", 0, strings.Repeat("evidence", 2000)},
		{"unlimited large", 0, strings.Repeat("中文🙂evidence", 10000)},
		{"unlimited lines", 0, strings.Repeat("evidence\n", 10000)},
		{"limit exceeds page length", 90000, "all evidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, emit := outputToolStore(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = w.Write([]byte(tc.content))
			}))
			defer server.Close()
			routeReaderFixture(t, server.URL)
			parent := workerproto.Command{SessionID: "session", RunID: "run", Webfetch: workerproto.WebfetchConfig{PreviewBytes: tc.limit}}
			call := provider.ToolCall{ID: "configured-fetch"}
			call.Function.Name, call.Function.Arguments = "webfetch", fmt.Sprintf(`{"url":%q}`, "https://93.184.216.34/fixture")
			output, err := (&toolRuntime{parent: parent, emit: emit}).execute(context.Background(), call)
			var result webfetchResult
			if err != nil || json.Unmarshal([]byte(output), &result) != nil || !utf8.ValidString(result.Content) || result.NextOffset != len(result.Content) || result.Bytes != int64(len(tc.content)) {
				t.Fatalf("configured initial output invalid: %+v %v", result, err)
			}
			want := tc.content
			if tc.limit > 0 {
				want = textPrefix(want, tc.limit)
			}
			if result.Content != want || result.Truncated != (want != tc.content) {
				t.Fatal("initial output did not honor the user's byte setting")
			}
			bounded, err := boundToolOutput(context.Background(), "run", call.ID, call.Function.Name, output, resultOutputRef(output), emit)
			if err != nil || bounded.model != output {
				t.Fatalf("tool pipeline overrode the user's byte setting: %v", err)
			}
			artifact, err := db.GetArtifact(context.Background(), "session", result.ArtifactID)
			if err != nil || !artifact.Complete || artifact.Bytes != int64(len(tc.content)) {
				t.Fatalf("initial output length changed the full saved page: %+v %v", artifact, err)
			}
			server.Close()
			var recovered strings.Builder
			recovered.WriteString(result.Content)
			for offset := int64(result.NextOffset); offset < result.Bytes; {
				output, err := readToolOutput(context.Background(), fmt.Sprintf(`{"artifactId":%q,"offset":%d,"limit":32768}`, result.ArtifactID, offset), parent)
				var page tools.OutputPage
				if err != nil || json.Unmarshal([]byte(output), &page) != nil || page.NextOffset <= offset {
					t.Fatalf("read after configured initial content: %v", err)
				}
				recovered.WriteString(page.Content)
				offset = page.NextOffset
			}
			if recovered.String() != tc.content || requests.Load() != 1 {
				t.Fatal("follow-up reads lost content or downloaded the page again")
			}
		})
	}
}

func TestWebfetchSmallPageReturnsCompleteSnapshot(t *testing.T) {
	_, emit := outputToolStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("small page 你好"))
	}))
	defer server.Close()
	routeReaderFixture(t, server.URL)
	call := provider.ToolCall{}
	call.Function.Name, call.Function.Arguments = "webfetch", fmt.Sprintf(`{"url":%q}`, "https://93.184.216.34/fixture")
	output, err := (&toolRuntime{parent: workerproto.Command{SessionID: "session", RunID: "run"}, emit: emit}).execute(context.Background(), call)
	var result webfetchResult
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || result.ArtifactID == "" || result.Truncated || !result.Complete || result.Content != "small page 你好" || result.Bytes != int64(len(result.Content)) {
		t.Fatalf("small page: %s, %v", output, err)
	}
}

func TestWebfetchIncompleteDownloadKeepsReadableArtifact(t *testing.T) {
	db, emit := outputToolStore(t)
	content := strings.Repeat("Saved before the connection failed.\n", 1000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprint(len(content)+1000))
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()
	routeReaderFixture(t, server.URL)
	call := provider.ToolCall{ID: "incomplete-fetch"}
	call.Function.Name, call.Function.Arguments = "webfetch", fmt.Sprintf(`{"url":%q}`, "https://93.184.216.34/fixture")
	parent := workerproto.Command{SessionID: "session", RunID: "run"}
	output, err := (&toolRuntime{parent: parent, emit: emit}).execute(context.Background(), call)
	var result webfetchResult
	if err == nil || json.Unmarshal([]byte(output), &result) != nil || result.ArtifactID == "" || result.Complete || result.Bytes != 0 {
		t.Fatalf("partial output metadata: bytes=%d, complete=%t, error=%v", result.Bytes, result.Complete, err)
	}
	artifact, err := db.GetArtifact(context.Background(), "session", result.ArtifactID)
	if err != nil || artifact.Complete || artifact.Status != "failed" {
		t.Fatalf("incomplete artifact: %+v, %v", artifact, err)
	}
	full, err := os.ReadFile(artifact.OutputPath)
	if err != nil || len(full) != 0 {
		t.Fatal("failed candidate contaminated the artifact")
	}
}

func TestWebfetchFailsWhenFullOutputCannotBeSaved(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("page", 50000)))
	}))
	defer server.Close()
	routeReaderFixture(t, server.URL)
	call := provider.ToolCall{ID: "fetch"}
	call.Function.Name, call.Function.Arguments = "webfetch", fmt.Sprintf(`{"url":%q}`, "https://93.184.216.34/fixture")
	failure := errors.New("artifact storage unavailable")
	runtime := &toolRuntime{parent: workerproto.Command{RunID: "run"}, emit: func(workerproto.Result) error { return failure }}
	output, err := runtime.execute(context.Background(), call)
	if !errors.Is(err, failure) || output != "" {
		t.Fatalf("fetch claimed a successful saved page: %s, %v", output, err)
	}
}
