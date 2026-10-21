package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

// A final answer can become intermediate history when an interjection arrives.
// Full text must survive on either side of the display-preview boundary;
// short replies also retain their existing native replay state.
func TestInterjectionKeepsFullTextAcrossAnswerPreviewBoundary(t *testing.T) {
	for _, size := range []int{8 << 10, (8 << 10) + 1} {
		t.Run(strconv.Itoa(size)+" bytes", func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			full := strings.Repeat("x", size)
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []map[string]any `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if requests.Add(1) == 1 {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Keep the state."}}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-state"}}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"text"}}`+"\n\n")
					chunk, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]string{"type": "text_delta", "text": full}})
					fmt.Fprintf(w, "data: %s\n\n", chunk)
					fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
					fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
					return
				}
				encoded, _ := json.Marshal(request.Messages)
				if size <= 8<<10 {
					if !strings.Contains(string(encoded), "opaque-state") || !strings.Contains(string(encoded), full) {
						t.Error("short answer lost its original response items on interjection")
					}
				} else if strings.Contains(string(encoded), "opaque-state") || !strings.Contains(string(encoded), full) || strings.Contains(string(encoded), "Full answer:") {
					t.Error("long answer lost its full text or leaked the display preview into model history")
				}
				if len(request.Messages) == 0 {
					t.Error("interjection request omitted all history")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				last := request.Messages[len(request.Messages)-1]
				if last["role"] != "user" || last["content"] != "Continue with the new constraint." {
					t.Error("interjection missing after the previous answer")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`+"\n\n")
				fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`+"\n\n")
				fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
				fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
				fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
			}))
			defer upstream.Close()
			inputs := make(chan workerproto.Command, 1)
			command := workerproto.Command{RunID: "boundary", Prompt: "Respond.", Model: modelcfg.Model{
				Provider: "anthropic", ID: "fixture", BaseURL: upstream.URL, APIKey: "test-key",
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sent := false
			answer, err := RunWithInputs(ctx, command, func(event workerproto.Result) error {
				if event.Kind == "model_result" && !sent {
					sent = true
					inputs <- workerproto.Command{RunID: command.RunID, InputID: "interjection", Prompt: "Continue with the new constraint."}
				}
				return nil
			}, inputs)
			if err != nil || answer != "done" || requests.Load() != 2 || !sent {
				t.Fatalf("answer=%q err=%v requests=%d interjected=%v", answer, err, requests.Load(), sent)
			}
		})
	}
}
