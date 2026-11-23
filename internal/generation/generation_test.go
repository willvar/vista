package generation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"vista/internal/model"
	"vista/internal/network"
)

func target(id, kind, base string) model.GenerationTarget {
	return model.GenerationTarget{Kind: kind, Model: model.Model{ProviderID: id, BaseURL: base, APIKey: "fixture-key", ID: "fixture-model", Network: &network.Policy{}}}
}

const clawRouterCatalogFixture = `{"version":"clawrouter.client-catalog.v1","providers":[{"id":"openai","allowed":true,"executable":true,"nativeBaseUrl":"/v1/native/openai","routes":[{"path":"/v1/decisions","requestFormat":"openai.decisions","methods":["POST"]}],"models":[{"id":"openai/gpt-image-2.5-flare","displayName":"GPT Image 2.5 Flare","capabilities":["image.generation"],"pricing":{"unit":"image_token","generation":{"maxPromptCharacters":32000,"maxImages":10,"dimensionMultiple":16,"minPixels":65536,"maxPixels":10000000,"maxEdge":3840,"maxAspectRatio":3}}},{"id":"openai/gpt-6-luna","upstream":"gpt-6-luna","capabilities":["llm.decisions"]}],"offers":[{"endpoint":"images_generations","modelId":"openai/gpt-image-2.5-flare","transport":"http","routeKind":"unified","route":"/v1/images/generations","eligible":true,"affordability":"request-dependent"}]}]}`

func TestDecisionTypesAndErrors(t *testing.T) {
	for _, valid := range []bool{true, false} {
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/systemone" || r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			var in wire
			_ = json.NewDecoder(r.Body).Decode(&in)
			if text(in, "questions", "boolean", "type") != "noul" || text(in, "state") != "evidence" {
				t.Error("wrong decision request")
			}
			if valid {
				io.WriteString(w, `{"answers":{"boolean":{"noul":0.8},"category":{"choice":"A"},"quality":{"score":0.75}}}`)
			} else {
				io.WriteString(w, `{"answers":{"boolean":{"error":"unavailable"}}}`)
			}
		}))
		out, err := Decide(t.Context(), target("openrouter", model.DecisionOutput, h.URL), "evidence", []Question{{"boolean", "boolean", "Test?", nil}, {"category", "choice", "Category?", []string{"A", "B"}}, {"quality", "score", "Quality?", []string{"bad", "good"}}})
		h.Close()
		if (err == nil) != valid || (len(out) > 0) != valid {
			t.Fatalf("valid=%v error=%v body=%s", valid, err, out)
		}
	}
}

func TestCanceledVideoDoesNotResubmit(t *testing.T) {
	started := make(chan struct{})
	var mu sync.Mutex
	var calls int
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			io.WriteString(w, `{"request_id":"original"}`)
			close(started)
			return
		}
		<-r.Context().Done()
	}))
	defer h.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wait := make(chan error, 1)
	go func() {
		_, err := Generate(ctx, target("xai", model.VideoOutput, h.URL), Input{Prompt: "test"})
		wait <- err
	}()
	<-started
	cancel()
	if err := <-wait; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("sent %d requests to provider after cancellation", calls)
	}
}

func TestFirstFrameRejectionDoesNotSubmit(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected media request"); w.WriteHeader(400) }))
	defer h.Close()
	for _, id := range []string{"deepinfra", "xai"} {
		cfg := target(id, model.VideoOutput, h.URL)
		if id == "xai" {
			cfg.InputModalities = []string{"text"}
		}
		if _, err := Generate(t.Context(), cfg, Input{Prompt: "clip", Frame: []byte("image"), FrameType: "image/png"}); err == nil {
			t.Fatal("text-only route accepted a frame")
		}
	}
}
