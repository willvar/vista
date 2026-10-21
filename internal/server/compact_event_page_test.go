package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"vista/internal/store"
)

func TestCompactExecutionEventPagesKeepTextMetadataAndOriginalBodies(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "compact-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const session = "00000000-0000-4000-8000-000000000061"
	const run = "00000000-0000-4000-8000-000000000062"
	if _, err := st.Create(ctx, session, "compact events"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginRun(ctx, session, run, "input", "test"); err != nil {
		t.Fatal(err)
	}
	targets := make(map[int64]store.Event)
	var sequences []int64
	for index, input := range []struct{ kind, content string }{
		{"tool", `{"output":"` + strings.Repeat("完整工具输出", 300) + `"}`},
		{"tool", strings.Repeat("a", 511) + "界" + strings.Repeat("b", 20)},
		{"tool", strings.Repeat("x", 512)},
		{"model_reasoning_delta", strings.Repeat("完整推理", 300)},
		{"model_delta", strings.Repeat("完整正文", 300)},
	} {
		data := map[string]any{
			"runId": run, "kind": input.kind, "turn": 7,
			"taskId": "main/child", "parentTaskId": "main", "depth": 1,
			"status": "succeeded", "content": input.content,
			"extraMetadata": map[string]any{"keep": "unchanged"},
		}
		if input.kind == "tool" {
			data["tool"] = map[string]any{
				"callId": fmt.Sprintf("call-%d", index), "name": "shell", "summary": "command result",
				"input":     map[string]any{"command": "echo result", "directory": "."},
				"outputRef": map[string]any{"path": "output.log", "bytes": len(input.content)},
			}
		}
		event, err := st.AppendRunEvent(ctx, session, run, input.kind, data)
		if err != nil {
			t.Fatal(err)
		}
		targets[event.Sequence] = *event
		sequences = append(sequences, event.Sequence)
	}
	app := &App{store: st}
	root := "/api/sessions/" + session
	paths := []string{
		fmt.Sprintf("%s/event-page?before=%d", root, sequences[len(sequences)-1]+1),
		fmt.Sprintf("%s/event-page?after=%d", root, sequences[0]-1),
		root + "/runs/" + run + "/turns/7/events?before=" + fmt.Sprint(sequences[len(sequences)-1]+1),
	}
	for routeIndex, path := range paths {
		for _, suffix := range []string{"", "&compact=0", "&compact=1"} {
			t.Run(fmt.Sprintf("route-%d%s", routeIndex, suffix), func(t *testing.T) {
				response := httptest.NewRecorder()
				app.session(response, httptest.NewRequest(http.MethodGet, path+suffix, nil))
				var page []store.Event
				if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil {
					t.Fatalf("page HTTP %d: %s", response.Code, response.Body.String())
				}
				seen := 0
				for index, event := range page {
					if index > 0 && page[index-1].Sequence >= event.Sequence {
						t.Fatalf("page order changed: %+v", page)
					}
					original, exists := targets[event.Sequence]
					if !exists {
						continue
					}
					seen++
					if event.SessionID != original.SessionID || event.Kind != original.Kind || event.CreatedAt != original.CreatedAt {
						t.Fatalf("event envelope changed: %+v", event)
					}
					var got, want map[string]any
					if err := json.Unmarshal(event.Data, &got); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(original.Data, &want); err != nil {
						t.Fatal(err)
					}
					content := want["content"].(string)
					if suffix == "&compact=1" && event.Kind == "tool" && len(content) > 512 {
						end := 512
						for !utf8.RuneStart(content[end]) {
							end--
						}
						want["content"] = content[:end]
						want["tool"].(map[string]any)["truncated"] = true
						if !utf8.ValidString(got["content"].(string)) {
							t.Fatal("preview split a UTF-8 character")
						}
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("compact changed unexpected content or metadata: got=%v want=%v", got, want)
					}
				}
				if seen != len(targets) {
					t.Fatalf("events missing: got %d want %d", seen, len(targets))
				}
			})
		}
	}
	// Reading compact pages must not affect stored originals or the exact-event route.
	for _, sequence := range sequences {
		response := httptest.NewRecorder()
		path := fmt.Sprintf("%s/runs/%s/events/%d?compact=1", root, run, sequence)
		app.session(response, httptest.NewRequest(http.MethodGet, path, nil))
		var recovered store.Event
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &recovered) != nil || string(recovered.Data) != string(targets[sequence].Data) {
			t.Fatalf("original body changed after compact page reads: sequence=%d HTTP %d", sequence, response.Code)
		}
	}
}
