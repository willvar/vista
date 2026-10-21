//go:build linux

package worker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestWorkerLoadsCatalogUpdatesAtExecutionBoundariesAndAfterRestart(t *testing.T) {
	root := t.TempDir()
	summaries := make(chan bool, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Reasoning map[string]string `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		summaries <- request.Reasoning["summary"] == "auto"
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer upstream.Close()
	input, results := startWorkerFixture(t, root)
	run := func(input *json.Encoder, results <-chan workerproto.Result, id string, wantSummary bool) {
		t.Helper()
		command := workerproto.Command{RunID: id, Prompt: "say done", Model: modelcfg.Model{Provider: "openai-responses", ProviderID: "custom", ID: "vista-omp-runtime", APIKey: "fixture", BaseURL: upstream.URL}}
		if err := input.Encode(command); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(5 * time.Second)
		for {
			select {
			case result, ok := <-results:
				if !ok {
					t.Fatal("Worker exited during catalog test")
				}
				if result.AckID != "" {
					if err := input.Encode(workerproto.Command{Action: "ack", RunID: id, AckID: result.AckID}); err != nil {
						t.Fatal(err)
					}
				}
				if result.RunID == id && result.Kind == "" {
					if result.Status != "succeeded" {
						t.Fatalf("run %s: %+v", id, result)
					}
					if got := <-summaries; got != wantSummary {
						t.Fatalf("run %s summary requested=%v, want %v", id, got, wantSummary)
					}
					return
				}
			case <-deadline:
				t.Fatal("Worker did not finish catalog test")
			}
		}
	}
	run(input, results, "before-update", false)
	configDir := filepath.Join(root, "vista")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"source":"oh-my-pi","revision":"ffffffffffffffffffffffffffffffffffffffff","modelCount":1,"providers":{"openai":{"vista-omp-runtime":{"name":"Runtime model","api":"openai-responses","input":["text"],"contextWindow":100000,"maxTokens":8192,"reasoning":true}}}}`)
	if err := os.WriteFile(provider.OMPCatalogPath(configDir), snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	run(input, results, "after-update", true)
	newInput, newResults := startWorkerFixture(t, root)
	run(newInput, newResults, "after-worker-restart", true)
	if err := os.WriteFile(provider.OMPCatalogPath(configDir), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	run(input, results, "after-corrupt-cache", true)
}
