package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestImageRequestsUseOnlyNewAttachmentsAndExplicitReads(t *testing.T) {
	db, ctx := checkpointFixture(t)
	original := resourcePNG(t)
	image := store.Image{MediaType: "image/png", Name: "original.png", Data: original}
	if _, _, err := db.BeginRunWithImages(ctx, "session", "old", "old-user", "Earlier image", []store.Image{image}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(ctx, "session", "old", "old-reply", "succeeded", "Earlier findings"); err != nil {
		t.Fatal(err)
	}
	current, _, err := db.BeginRunWithImages(ctx, "session", "current", "current-user", "Inspect again", []store.Image{image})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := db.MessageResources(ctx, "session", current.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("resource references: %+v %v", refs, err)
	}
	path := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(path, []byte("verified detail"), 0600); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]string{"path": path})
	encoded := []byte(base64.StdEncoding.EncodeToString(original))
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		want := 0
		if requests == 1 || requests == 3 || requests == 5 {
			want = 1
		}
		if count := bytes.Count(raw, encoded); count != want {
			t.Errorf("request %d: got %d image payloads, want %d", requests, count, want)
		}
		if !bytes.Contains(raw, []byte(refs[0].Hash)) {
			t.Errorf("request %d lost the resource hash", requests)
		}
		switch requests {
		case 1, 3, 5:
			continuationToolCalls(w, scriptedToolCall(fmt.Sprint("file-", requests), "read_file", string(arguments)))
		case 2:
			if !bytes.Contains(raw, []byte("verified detail")) || !bytes.Contains(raw, []byte("use read_resource")) {
				t.Error("ordinary tool continuation lost evidence or resource read instructions")
			}
			continuationToolCalls(w, scriptedToolCall("reread", "read_resource", `{"hash":"`+refs[0].Hash+`"}`))
		case 4, 6:
			historyTextResponse(w, "Finished.")
		default:
			t.Errorf("unexpected request %d", requests)
			historyTextResponse(w, "Unexpected.")
		}
	}))
	defer upstream.Close()
	command := workerproto.Command{SessionID: "session", RunID: "current", MessageSequence: current.Sequence,
		Prompt: "Inspect again", Images: refs,
		Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}}
	delivered := false
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	answer, err := RunWithInputSource(ctx, command, func(workerproto.Result) error { return nil }, func(reply *workerproto.CompletedReply) (*workerproto.Command, error) {
		if reply == nil || delivered {
			return nil, nil
		}
		delivered = true
		return &workerproto.Command{RunID: command.RunID, InputID: "interjection", Prompt: "Another image", Images: refs}, nil
	}, nil)
	if err != nil || answer != "Finished." || requests != 6 || !delivered {
		t.Fatalf("answer=%q requests=%d interjected=%v err=%v", answer, requests, delivered, err)
	}
}

func TestImageInputSurvivesOverflowRecoveryWithoutReactivatingHistory(t *testing.T) {
	db, ctx := checkpointFixture(t)
	original := resourcePNG(t)
	image := store.Image{MediaType: "image/png", Name: "original.png", Data: original}
	if _, _, err := db.BeginRunWithImages(ctx, "session", "old", "old-user", strings.Repeat("earlier verified detail ", 400), []store.Image{image}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(ctx, "session", "old", "old-reply", "succeeded", "Earlier findings"); err != nil {
		t.Fatal(err)
	}
	current, _, err := db.BeginRunWithImages(ctx, "session", "current", "current-user", "Inspect now", []store.Image{image})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := db.MessageResources(ctx, "session", current.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("resource references: %+v %v", refs, err)
	}
	path := filepath.Join(t.TempDir(), "evidence.txt")
	if err := os.WriteFile(path, []byte("verified detail"), 0600); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]string{"path": path})
	encoded := []byte(base64.StdEncoding.EncodeToString(original))
	requests, summaries := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if bytes.Contains(raw, []byte("Compress the completed execution transcript")) {
			summaries++
			if bytes.Contains(raw, encoded) {
				t.Error("summary request loaded image bytes")
			}
			historyTextResponse(w, "Earlier findings and retained resource hashes.")
			return
		}
		requests++
		want := 1
		if requests > 2 {
			want = 0
		}
		if count := bytes.Count(raw, encoded); count != want {
			t.Errorf("request %d: got %d images, want %d", requests, count, want)
		}
		switch requests {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":"context_length_exceeded"}}`)
		case 2:
			continuationToolCalls(w, scriptedToolCall("file", "read_file", string(arguments)))
		default:
			historyTextResponse(w, "Recovered.")
		}
	}))
	defer upstream.Close()
	command := workerproto.Command{SessionID: "session", RunID: "current", MessageSequence: current.Sequence,
		Prompt: "Inspect now", Images: refs,
		Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	answer, err := Run(ctx, command, func(workerproto.Result) error { return nil })
	if err != nil || answer != "Recovered." || requests != 3 || summaries == 0 {
		t.Fatalf("answer=%q requests=%d summaries=%d err=%v", answer, requests, summaries, err)
	}
}

func TestDelegationLeavesParentImagesAvailableForExplicitReads(t *testing.T) {
	db, ctx := checkpointFixture(t)
	user, _, err := db.BeginRunWithImages(ctx, "session", "run", "user", "Inspect", []store.Image{{MediaType: "image/png", Data: resourcePNG(t)}})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := db.MessageResources(ctx, "session", user.ID)
	if err != nil || len(refs) != 1 {
		t.Fatalf("resource references: %+v %v", refs, err)
	}
	scope, err := newRunScope(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.delegations.close()
	emit := func(workerproto.Result) error { return nil }
	runtime := &toolRuntime{scope: scope, emit: emit,
		parent: workerproto.Command{SessionID: "session", RunID: "run", TaskID: "main", Images: refs},
		runChild: func(ctx context.Context, child workerproto.Command, _ func(workerproto.Result) error) (string, error) {
			if len(child.Images) != 0 {
				t.Error("delegation automatically reactivated the parent's images")
			}
			reader := &toolRuntime{parent: child}
			reader.parent.Model.Provider = "openai-chat"
			if _, err := reader.readResource(ctx, resourceCall("read_resource", `{"hash":"`+refs[0].Hash+`"}`)); err != nil {
				t.Errorf("child cannot explicitly read the original: %v", err)
			}
			return "Reviewed.", nil
		}}
	if _, err := runtime.delegate(ctx, resourceCall("delegate", `{"task":"Inspect the retained resource if needed"}`)); err != nil {
		t.Fatal(err)
	}
	results, err := scope.delegations.joinRequired(ctx, "main")
	if err != nil || len(results) != 1 || results[0].Status != "succeeded" {
		t.Fatalf("join results: %+v %v", results, err)
	}
}
