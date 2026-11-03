package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	modelcfg "vista/internal/model"
)

func TestUsageRecorderCoversPurposesFailuresAndMissingUsageOnce(t *testing.T) {
	for _, scenario := range []string{"conversation", "compaction", "title", "missing", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "failed" {
					http.Error(w, "unavailable", 503)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
				if scenario != "missing" {
					fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":25,\"prompt_tokens_details\":{\"cached_tokens\":50},\"completion_tokens_details\":{\"reasoning_tokens\":10}}}\n\n")
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			calls := 0
			ctx := WithUsageRecorder(context.Background(), func(id string, tokens *int64) {
				calls++
				if _, err := uuid.Parse(id); err != nil {
					t.Fatal(err)
				}
				if scenario == "missing" || scenario == "failed" {
					if tokens != nil {
						t.Fatal("invented missing usage", *tokens)
					}
					return
				}
				if tokens == nil || *tokens != 125 {
					t.Fatal("duplicate cache/reasoning or missing usage", tokens)
				}
			})
			model := modelcfg.Model{Provider: "openai-chat", ID: "test", APIKey: "test", BaseURL: server.URL}
			if scenario == "title" {
				model = ForPurpose(model, PurposeTitle)
			}
			if scenario == "compaction" {
				model = ForPurpose(model, PurposeCompaction)
			}
			_, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if (err != nil) != (scenario == "failed") || calls != 1 {
				t.Fatal("wrong collection count", calls, err)
			}
		})
	}
}
