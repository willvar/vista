package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func resourcePNG(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestResourceRequestsRetainOriginalsAcrossModelSwitchesAndReferenceOnlyCheckpoints(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := context.Background()
	original := resourcePNG(t)
	ref := putResourceFixture(t, original, "image/png")
	ref.Name = "细节.png"
	messages := []provider.ChatMessage{{Role: "system", Content: "instructions"}, {Role: "user", Content: "inspect", Resources: []resources.Ref{ref}}}
	messages[1] = messages[1].WithVisualInput(true)
	command := workerproto.Command{Model: modelcfg.Model{Provider: "openai-chat"}}
	for _, accepts := range []bool{true, false, true} {
		command.Model.ImageInput = &accepts
		request, err := materializeResources(ctx, command, messages)
		if err != nil {
			t.Fatal(err)
		}
		if accepts && (len(request[1].Images) != 1 || !bytes.Equal(request[1].Images[0].Data, original)) {
			t.Fatal("model switch lost original visual input")
		}
		if !accepts && (len(request[1].Images) != 0 || !strings.Contains(request[1].Content, "omitted from this request")) {
			t.Fatal("text-only model received images or no omission notice")
		}
		if messages[1].Content != "inspect" || len(messages[1].Images) != 0 || !reflect.DeepEqual(messages[1].Resources, []resources.Ref{ref}) {
			t.Fatal("request materialization mutated canonical context")
		}
		if _, err := saveContextCheckpoint(command, request); accepts && err == nil {
			t.Fatal("checkpoint accepted request-local image bytes")
		}
	}
	cp, err := saveContextCheckpoint(command, messages)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cp.Path)
	if err != nil || bytes.Contains(data, original) || bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString(original))) || !bytes.Contains(data, []byte(ref.Hash)) {
		t.Fatalf("checkpoint must store the reference only: %v", err)
	}
	root, err := os.OpenRoot(filepath.Dir(cp.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	history, err := loadContextFile(root, cp.Path, command)
	if err != nil || len(history) != 1 || history[0].VisualInput() {
		t.Fatalf("checkpoint reactivated historical image: %+v %v", history, err)
	}
	request, err := materializeResources(ctx, command, history)
	if err != nil || len(request[0].Images) != 0 || !strings.Contains(request[0].Content, "use read_resource") {
		t.Fatalf("restored history automatically sent image bytes: %+v %v", request, err)
	}
}

func TestHistoricalResourceMetadataDoesNotReadTheOriginal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ref := resources.Ref{Hash: strings.Repeat("a", 64), Name: "historical.png", MediaType: "image/png", Bytes: 1 << 20}
	messages := []provider.ChatMessage{{Role: "user", Content: "Earlier image", Resources: []resources.Ref{ref}}}
	command := workerproto.Command{Model: modelcfg.Model{Provider: "openai-chat"}}
	request, err := materializeResources(context.Background(), command, messages)
	if err != nil || len(request[0].Images) != 0 || !strings.Contains(request[0].Content, ref.Hash) || !strings.Contains(request[0].Content, "use read_resource") {
		t.Fatalf("metadata request read a historical file: %+v %v", request, err)
	}
	if _, err := materializeResources(context.Background(), command, []provider.ChatMessage{messages[0].WithVisualInput(true)}); err == nil {
		t.Fatal("activated image did not read and validate its original")
	}
}

func TestCompactedHistoricalImageCanBeListedAndRereadWithoutAddingChatMessages(t *testing.T) {
	db, ctx := checkpointFixture(t)
	original := resourcePNG(t)
	old, _, err := db.BeginRunWithImages(ctx, "session", "old", "old-user", "Original picture. "+strings.Repeat("verified detail ", 300), []store.Image{{MediaType: "image/png", Name: "original.png", Data: original}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(ctx, "session", "old", "old-reply", "succeeded", "Saved findings."); err != nil {
		t.Fatal(err)
	}
	current, _, err := db.BeginRun(ctx, "session", "current", "current-user", "Look again")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := db.MessageResources(ctx, "session", old.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("original references: %+v %v", refs, err)
	}
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"read-image\",\"type\":\"function\",\"function\":{\"name\":\"read_resource\",\"arguments\":\"{\\\"hash\\\":\\\""+refs[0].Hash+"\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
		} else {
			last := input.Messages[len(input.Messages)-1]
			if last.Role != "user" || !bytes.Contains(last.Content, []byte(base64.StdEncoding.EncodeToString(original))) || input.Messages[len(input.Messages)-2].Role != "tool" {
				t.Errorf("resource tool did not send the original as visual input: %+v", last)
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Inspected original.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer upstream.Close()
	command := workerproto.Command{SessionID: "session", RunID: "current", MessageSequence: current.Sequence, Prompt: "Look again", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}}
	// Simulate the committed result of compaction, with the old attachment absent
	// from retained context. The session ownership is independent of this file.
	checkpoint, err := saveContextCheckpoint(command, []provider.ChatMessage{{Role: "system"}, {Role: "assistant", Content: executionSummaryPrefix + "Earlier image was inspected; use list_resources to retrieve it."}, {Role: "user", Content: "Look again"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistCheckpoint(db, ctx, command, checkpoint); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConversationContext(ctx, command, 0)
	if err != nil || len(loaded) != 2 || len(loaded[0].Resources) != 0 {
		t.Fatalf("checkpoint did not omit historical image: %+v %v", loaded, err)
	}
	runtime := &toolRuntime{parent: command}
	listed, err := runtime.listResources(ctx, resourceCall("list_resources", `{}`))
	if err != nil || !strings.Contains(listed, refs[0].Hash) || !strings.Contains(listed, "original.png") {
		t.Fatalf("compacted resource could not be discovered: %s %v", listed, err)
	}
	before, _ := db.MessagesBefore(ctx, "session", 0, 20)
	answer, err := Run(ctx, command, func(workerproto.Result) error { return nil })
	if err != nil || answer != "Inspected original." || requests != 2 {
		t.Fatalf("reread execution: answer=%q requests=%d err=%v", answer, requests, err)
	}
	after, _ := db.MessagesBefore(ctx, "session", 0, 20)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("resource reread created a fake user chat message")
	}
	if _, err := db.Create(ctx, "foreign", "other session"); err != nil {
		t.Fatal(err)
	}
	runtime.parent.SessionID = "foreign"
	if _, err := runtime.readResource(ctx, resourceCall("read_resource", `{"hash":"`+refs[0].Hash+`"}`)); err == nil {
		t.Fatal("resource read crossed session ownership")
	}
	runtime.parent = command
	accepts := false
	runtime.parent.Model.ImageInput = &accepts
	if _, err := runtime.readResource(ctx, resourceCall("read_resource", `{"hash":"`+refs[0].Hash+`"}`)); err == nil {
		t.Fatal("text-only model pretended to inspect the resource")
	}
}

func resourceCall(name, arguments string) provider.ToolCall {
	call := provider.ToolCall{ID: "resource", Type: "function"}
	call.Function.Name, call.Function.Arguments = name, arguments
	return call
}
