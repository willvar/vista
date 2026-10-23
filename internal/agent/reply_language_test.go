package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/workerproto"
)

func TestInterjectionChangesDefaultReplyLanguageOnNextRequest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) == 0 {
			t.Errorf("request: %v", err)
			w.WriteHeader(400)
			return
		}
		want := "Traditional Chinese (Hong Kong)"
		if calls.Add(1) == 2 {
			want = "Japanese"
		}
		if prompt := body.Messages[0].Content; strings.Count(prompt, "Default to replies in") != 1 || !strings.Contains(prompt, "Default to replies in "+want) {
			t.Errorf("language missing or stale: %s", prompt)
		}
		continuationAnswer(w, "done")
	}))
	defer upstream.Close()
	command := continuationCommand(upstream.URL, "Start")
	command.ReplyLanguage = "zh-HK"
	inputs := make(chan workerproto.Command, 1)
	answer, err := RunWithInputs(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "model_result" && event.Turn == 1 {
			inputs <- workerproto.Command{RunID: command.RunID, InputID: "next", Prompt: "Continue", ReplyLanguage: "ja"}
		}
		return nil
	}, inputs)
	if err != nil || answer != "done" || calls.Load() != 2 {
		t.Fatalf("answer=%q calls=%d error=%v", answer, calls.Load(), err)
	}
}

func TestReplyLanguageReachesAutomaticAndManualCompaction(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%t", manual), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			var calls, summaries atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []struct{ Role, Content string }
					Tools    []any
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
					t.Errorf("request: %v", err)
					w.WriteHeader(400)
					return
				}
				prompt := request.Messages[0].Content
				if strings.Count(prompt, "Default to replies in") != 1 || !strings.Contains(prompt, "Default to replies in European Portuguese; follow the user's explicit language requests.") {
					t.Errorf("compaction or retry lost the language policy: %s", prompt)
				}
				if strings.HasPrefix(prompt, executionSummaryPrompt) {
					summaries.Add(1)
					if len(request.Tools) != 0 {
						t.Error("summary request exposed tools")
					}
					continuationAnswer(w, "Earlier evidence retained.")
					return
				}
				if calls.Add(1) == 1 {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"code":"context_length_exceeded"}}`)
					return
				}
				if encoded, _ := json.Marshal(request.Messages); !strings.Contains(string(encoded), "Earlier evidence retained.") || !strings.Contains(string(encoded), "Please answer in French.") {
					t.Error("retry lost the summary or explicit user language request")
				}
				continuationAnswer(w, "done")
			}))
			defer upstream.Close()
			command := continuationCommand(upstream.URL, "Please answer in French.")
			command.ReplyLanguage = "pt-PT"
			command.History = []workerproto.Message{
				{Role: "user", Content: "Earlier task"},
				{Role: "assistant", Content: strings.Repeat("Verified earlier evidence. ", 1200)},
			}
			emit := func(workerproto.Result) error { return nil }
			if manual {
				if err := CompactSession(context.Background(), command, emit); err != nil {
					t.Fatal(err)
				}
			} else if answer, err := Run(context.Background(), command, emit); err != nil || answer != "done" {
				t.Fatalf("overflow recovery: answer=%q error=%v", answer, err)
			}
			if summaries.Load() == 0 || manual && calls.Load() != 0 || !manual && calls.Load() != 2 {
				t.Fatalf("unexpected flow: normal=%d summaries=%d", calls.Load(), summaries.Load())
			}
		})
	}
}
