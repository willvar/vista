package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"vista/internal/tools"
	"vista/internal/workerproto"
)

type fileReadPageForTest struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Content    string `json:"content"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	LineCount  int    `json:"lineCount"`
	TotalLines int    `json:"totalLines"`
	NextLine   int    `json:"nextLine"`
	Bytes      int    `json:"bytes"`
	Truncated  bool   `json:"truncated"`
}

func readFilePageForTest(t *testing.T, path string, options map[string]any) (fileReadPageForTest, string) {
	t.Helper()
	arguments := map[string]any{"path": path}
	for key, value := range options {
		arguments[key] = value
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&toolRuntime{}).execute(context.Background(), scriptedToolCall("read", "read_file", string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var page fileReadPageForTest
	if err := json.Unmarshal([]byte(output), &page); err != nil {
		t.Fatal(err)
	}
	return page, output
}

func TestReadFileUsesSearchLineNumbersAndFullFileHash(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.txt")
	lines := make([]string, 400)
	for i := range lines {
		lines[i] = fmt.Sprintf("source line %d", i+1)
	}
	lines[279] = "target function"
	full := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(full), 0600); err != nil {
		t.Fatal(err)
	}
	matches, err := tools.Search(context.Background(), root, "target function")
	if err != nil || len(matches.Matches) != 1 {
		t.Fatalf("search: %+v, %v", matches, err)
	}
	match := matches.Matches[0]
	page, output := readFilePageForTest(t, match.Path, map[string]any{"startLine": match.Line, "lineCount": 3})
	if page.Content != "280: target function\n281: source line 281\n282: source line 282" || page.StartLine != 280 || page.EndLine != 282 || page.LineCount != 3 || page.TotalLines != 400 || page.NextLine != 283 || !page.Truncated {
		t.Fatalf("search and read coordinates differ: %+v", page)
	}
	hash := sha256.Sum256([]byte(full))
	if page.SHA256 != hex.EncodeToString(hash[:]) || page.Bytes != len(full) || page.Path != path {
		t.Fatalf("partial read lost full-file identity: %+v", page)
	}
	var fields map[string]any
	_ = json.Unmarshal([]byte(output), &fields)
	for _, key := range []string{"offset", "limit", "nextOffset"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("read_file still exposes byte pagination: %s", output)
		}
	}
	if _, err := tools.WriteFile(context.Background(), filepath.Join(root, "state"), path, page.SHA256, "updated"); err != nil {
		t.Fatalf("partial read hash did not authorize a write: %v", err)
	}
}

func TestReadFileDefaultsTo200LinesAndContinuesWithoutGaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	lines := make([]string, 450)
	for i := range lines {
		lines[i] = fmt.Sprintf("第 %d 行 🙂", i+1)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	for start := 1; start <= len(lines); {
		options := map[string]any{}
		if start != 1 {
			options["startLine"] = start
		}
		page, output := readFilePageForTest(t, path, options)
		end := min(start+199, len(lines))
		want := make([]string, 0, end-start+1)
		for line := start; line <= end; line++ {
			want = append(want, fmt.Sprintf("%d: %s", line, lines[line-1]))
		}
		if page.StartLine != start || page.EndLine != end || page.LineCount != len(want) || page.TotalLines != len(lines) || page.Content != strings.Join(want, "\n") || exceedsToolBudget(output) {
			t.Fatalf("incorrect line page at %d: %+v", start, page)
		}
		if end == len(lines) {
			if page.Truncated || page.NextLine != 0 || strings.Contains(output, `"nextLine"`) {
				t.Fatalf("EOF still advertises continuation: %s", output)
			}
			break
		}
		if !page.Truncated || page.NextLine != end+1 {
			t.Fatalf("page cannot continue: %+v", page)
		}
		start = page.NextLine
	}
}

func TestReadFileKeepsLineCoordinatesForEmptyAndCRLFText(t *testing.T) {
	for _, test := range []struct {
		name, full, want string
		total            int
	}{
		{"empty", "", "1: ", 1},
		{"blank-lines", "\n\nlast", "1: \n2: \n3: last", 3},
		{"trailing-newline", "first\n", "1: first\n2: ", 2},
		{"crlf", "first\r\n第二行🙂\r\nlast", "1: first\r\n2: 第二行🙂\r\n3: last", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.txt")
			if err := os.WriteFile(path, []byte(test.full), 0600); err != nil {
				t.Fatal(err)
			}
			page, _ := readFilePageForTest(t, path, nil)
			if page.Content != test.want || page.StartLine != 1 || page.EndLine != test.total || page.LineCount != test.total || page.TotalLines != test.total || page.Truncated || page.NextLine != 0 {
				t.Fatalf("incorrect empty/newline handling: %+v", page)
			}
		})
	}
}

func TestReadFileRejectsInvalidLineArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("first\nlast"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, options := range []string{
		`"startLine":0`, `"startLine":-1`, `"startLine":3`, `"startLine":1.5`,
		`"lineCount":0`, `"lineCount":-1`, `"lineCount":1.5`,
		`"offset":1`, `"limit":80`,
	} {
		t.Run(options, func(t *testing.T) {
			arguments := fmt.Sprintf(`{"path":%q,%s}`, path, options)
			if output, err := (&toolRuntime{}).execute(context.Background(), scriptedToolCall("read", "read_file", arguments)); err == nil {
				t.Fatalf("invalid line arguments accepted: %s", output)
			}
		})
	}
	page, _ := readFilePageForTest(t, path, map[string]any{"startLine": 2, "lineCount": int(^uint(0) >> 1)})
	if page.Content != "2: last" || page.LineCount != 1 || page.EndLine != 2 || page.Truncated {
		t.Fatalf("large count overflowed instead of stopping at EOF: %+v", page)
	}
}

func TestReadFileFitsEscapedOutputAtWholeLineBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "escaped.txt")
	lines := make([]string, 600)
	for i := range lines {
		lines[i] = fmt.Sprintf("%d %s", i+1, strings.Repeat("<\x00\t🙂", 100))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	pages := 0
	for start := 1; start <= len(lines); {
		page, output := readFilePageForTest(t, path, map[string]any{"startLine": start, "lineCount": 10000})
		if page.StartLine != start || page.LineCount <= 0 || page.EndLine != start+page.LineCount-1 || exceedsToolBudget(output) || !utf8.ValidString(page.Content) {
			t.Fatalf("invalid bounded page at %d: lines=%d end=%d bytes=%d", start, page.LineCount, page.EndLine, len(output))
		}
		got := strings.Split(page.Content, "\n")
		if len(got) != page.LineCount {
			t.Fatalf("line count differs from actual content: %d != %d", len(got), page.LineCount)
		}
		for i, line := range got {
			if line != fmt.Sprintf("%d: %s", start+i, lines[start+i-1]) {
				t.Fatalf("source line %d was cut or skipped", start+i)
			}
		}
		pages++
		if !page.Truncated {
			if page.EndLine != len(lines) || page.NextLine != 0 {
				t.Fatalf("incomplete final page: %+v", page)
			}
			break
		}
		if page.NextLine != page.EndLine+1 {
			t.Fatalf("nextLine skips content: %+v", page)
		}
		start = page.NextLine
	}
	if pages <= 1 {
		t.Fatal("fixture did not exercise output-budget pagination")
	}
}

func TestReadFileLargeLinePreservesArtifactRecoveryAndCoordinates(t *testing.T) {
	_, emit := outputToolStore(t)
	path := filepath.Join(t.TempDir(), "large-line.txt")
	longLine := strings.Repeat("完整<\x00🙂", 10000)
	full := "before\n" + longLine + "\nafter\nlast"
	if err := os.WriteFile(path, []byte(full), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": path, "startLine": 2, "lineCount": 2})
	output, err := (&toolRuntime{parent: workerproto.Command{SessionID: "session", RunID: "run"}, emit: emit}).execute(context.Background(), scriptedToolCall("large-read", "read_file", string(args)))
	if err != nil {
		t.Fatal(err)
	}
	var page fileReadPageForTest
	_ = json.Unmarshal([]byte(output), &page)
	bounded, err := boundToolOutput(context.Background(), "run", "large-read", "read_file", output, resultOutputRef(output), emit)
	if err != nil || bounded.reference == nil || !strings.Contains(output, "artifactId") || exceedsToolBudget(bounded.model) {
		t.Fatalf("large line did not become recoverable output: %v", err)
	}
	var preview struct {
		fileReadPageForTest
		ArtifactID string `json:"artifactId"`
		Hint       string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(bounded.model), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.ArtifactID != bounded.reference.ArtifactID || preview.Path != path || preview.SHA256 != page.SHA256 || preview.StartLine != 2 || preview.EndLine != 2 || preview.LineCount != 1 || preview.TotalLines != 4 || preview.NextLine != 3 || !strings.Contains(preview.Hint, "nextLine") {
		t.Fatalf("large line preview lost source/recovery coordinates: %+v", preview.fileReadPageForTest)
	}
	var recovered strings.Builder
	for offset := int64(0); offset < bounded.reference.Bytes; {
		arguments := fmt.Sprintf(`{"artifactId":%q,"offset":%d,"limit":32768}`, preview.ArtifactID, offset)
		text, err := readToolOutput(context.Background(), arguments, workerproto.Command{SessionID: "session", RunID: "run"})
		var artifactPage tools.OutputPage
		if err != nil || json.Unmarshal([]byte(text), &artifactPage) != nil || artifactPage.NextOffset <= offset || exceedsToolBudget(text) {
			t.Fatalf("large line artifact cannot continue: %v", err)
		}
		recovered.WriteString(artifactPage.Content)
		offset = artifactPage.NextOffset
	}
	var restored fileReadPageForTest
	if err := json.Unmarshal([]byte(recovered.String()), &restored); err != nil || restored.Content != "2: "+longLine || restored.SHA256 != page.SHA256 {
		t.Fatalf("large source line lost details: %v", err)
	}
	next, _ := readFilePageForTest(t, path, map[string]any{"startLine": preview.NextLine})
	if next.Content != "3: after\n4: last" || next.Truncated || next.NextLine != 0 {
		t.Fatalf("continuation skipped source lines: %+v", next)
	}
}

func TestReadFileStreamsMultiMiBSingleLineAndRecoversAllBytes(t *testing.T) {
	db, emit := outputToolStore(t)
	path := filepath.Join(t.TempDir(), "huge.txt")
	long := strings.Repeat("完整🙂<\x00", 200000)
	content := "before\n" + long + "\nafter"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]any{"path": path, "startLine": 2, "lineCount": 2})
	output, err := (&toolRuntime{parent: workerproto.Command{SessionID: "session", RunID: "run"}, emit: emit}).readFile(context.Background(), scriptedToolCall("huge-read", "read_file", string(arguments)))
	if err != nil || exceedsToolBudget(output) {
		t.Fatalf("unbounded read: %v", err)
	}
	var preview struct {
		fileReadPageForTest
		ArtifactID string `json:"artifactId"`
	}
	if err = json.Unmarshal([]byte(output), &preview); err != nil || preview.ArtifactID == "" || preview.EndLine != 2 || preview.NextLine != 3 || preview.TotalLines != 3 {
		t.Fatalf("missing line recovery: %s %v", output, err)
	}
	artifact, err := db.GetArtifact(context.Background(), "session", preview.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(artifact.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	var full fileReadPageForTest
	if err = json.Unmarshal(saved, &full); err != nil || full.Content != "2: "+long || full.Bytes != len(content) {
		t.Fatalf("large line lost bytes: %v", err)
	}
}
