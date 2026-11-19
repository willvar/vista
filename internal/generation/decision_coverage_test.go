package generation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/model"
)

func TestLockedOpenClawProviderCoverage(t *testing.T) {
	// Independent manifest contracts from e13cf15. Extra explicit Console and
	// Codex account routes are exercised too; no built-in silently loses picks.
	contracts := map[string][]string{
		model.ImageOutput:    {"clawrouter", "comfy", "deepinfra", "fal", "google", "litellm", "microsoft-foundry", "minimax", "minimax-portal", "openai", "openrouter", "vydra", "xai", "openai-codex"},
		model.VideoOutput:    {"alibaba", "byteplus", "comfy", "deepinfra", "fal", "google", "kie", "minimax", "minimax-portal", "novita", "openrouter", "pixverse", "qwen", "runway", "together", "vydra", "xai", "zai"},
		model.DecisionOutput: {"baseten", "clawrouter", "google", "levanto", "liquid-ai", "onnx", "openai", "openrouter", "subq", "typesafe", "opencode"},
	}
	for kind, want := range contracts {
		var ids []string
		for _, spec := range Specs() {
			if slices.Contains(spec.Kinds, kind) {
				ids = append(ids, spec.ID)
			}
		}
		slices.Sort(ids)
		slices.Sort(want)
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("%s routes are %v, want %v", kind, ids, want)
		}
		for _, id := range ids {
			if !NeedsInventory(id) && id != "microsoft-foundry" && len(Choices(id, kind)) == 0 {
				t.Errorf("%s advertises %s but returns no published models", id, kind)
			}
		}
	}
	if _, ok := Find("opencode-go", model.DecisionOutput); ok {
		t.Fatal("Go cannot fall back to Console decisions")
	}
}

func TestDecisionProviderWireDialects(t *testing.T) {
	const systemOne = `{"model":"fixture-model","answers":{"verify":{"type":"noul","noul":0.8},"color":{"type":"choice","choice":"red"},"quality":{"type":"score","score":0.75,"legend":{"0":"poor","1":"good"}}},"usage":{"input_tokens":4,"output_tokens":0}}`
	const google = `{"answers":{"verify":{"type":"noul","noul":0.8},"color":{"type":"choice","choice":"red"},"quality":{"type":"score","score":0.75,"legend":{"0":"poor","1":"good"}}},"usage":{"total_input_tokens":4,"total_output_tokens":0}}`
	const openai = `{"model":"gpt-6-luna","answers":[{"name":"verify","type":"predicate","probability":0.8},{"name":"color","type":"choice","choice":"red","confidence":0.75,"probabilities":[{"value":"red","probability":0.75},{"value":"blue","probability":0.25}]},{"name":"quality","type":"score","score":0.75,"confidence":0.75,"probabilities":[{"label":"0","value":0,"probability":0.25},{"label":"1","value":1,"probability":0.75}]}]}`
	const sage = `{"results":[{"answers":[{"ok":true,"result":{"id":"verify","kind":"yesno","result":{"answer":"yes","probability":0.8}}},{"ok":true,"result":{"id":"color","kind":"choice","result":{"chosen":"red"}}},{"ok":true,"result":{"id":"quality","kind":"scale","result":{"expectation":0.75}}}]}],"meta":{"model":"levanto-sage"}}`
	questions := []Question{{"verify", "boolean", "Check?", nil}, {"color", "choice", "Which?", []string{"red", "blue"}}, {"quality", "score", "Quality?", []string{"poor", "good"}}}
	for _, tc := range []struct{ id, path, reply string }{
		{"openai", "/decisions", openai}, {"clawrouter", "/native/openai/v1/decisions", openai}, {"google", "/decisions", google}, {"levanto", "/decide/batch", sage},
		{"openrouter", "/systemone", systemOne}, {"onnx", "/systemone", systemOne}, {"opencode", "/systemone", systemOne}, {"typesafe", "/systemone", systemOne}, {"liquid-ai", "/systemone", systemOne}, {"baseten", "/systemone", systemOne}, {"subq", "/systemone", systemOne},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var posts atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.id == "clawrouter" && r.Method == "GET" && r.URL.Path == "/catalog" {
					io.WriteString(w, clawRouterCatalogFixture)
					return
				}
				if r.Method != "POST" || r.URL.Path != tc.path {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				posts.Add(1)
				var in wire
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
					t.Error(err)
					return
				}
				switch tc.id {
				case "openai", "clawrouter":
					if text(in, "input") != "short evidence" || text(in, "questions", 0, "type") != "predicate" || text(in, "questions", 2, "levels", 1, "label") != "1" {
						t.Errorf("incorrect native body: %v", in)
					}
					if tc.id == "clawrouter" && text(in, "model") != "gpt-6-luna" {
						t.Error("incorrect ClawRouter native ID")
					}
				case "google":
					if r.Header.Get("x-goog-api-key") != "fixture-key" || r.Header.Get("Authorization") != "" || text(in, "input") != "short evidence" || text(in, "questions", "verify", "type") != "noul" {
						t.Error("incorrect Gemini decision request")
					}
				case "levanto":
					if text(in, "requests", 0, "content") != "short evidence" || text(in, "requests", 0, "questions", 2, "kind") != "scale" || in["model"] != nil {
						t.Error("incorrect Sage batch")
					}
				default:
					if text(in, "state") != "short evidence" || text(in, "questions", "verify", "type") != "noul" {
						t.Error("incorrect System One body")
					}
					if tc.id == "onnx" && r.Header.Get("Authorization") != "" {
						t.Error("local ONNX received credentials")
					}
				}
				io.WriteString(w, tc.reply)
			}))
			defer h.Close()
			setup := target(tc.id, model.DecisionOutput, h.URL)
			if tc.id == "clawrouter" {
				setup.Model.ID = "openai/gpt-6-luna"
			}
			if tc.id == "onnx" {
				setup.Model.APIKey = ""
			}
			out, err := Decide(t.Context(), setup, "short evidence", questions)
			if err != nil || posts.Load() != 1 {
				t.Fatalf("execution error=%v submissions=%d", err, posts.Load())
			}
			if !strings.Contains(string(out), `"noul":0.8`) || !strings.Contains(string(out), `"choice":"red"`) {
				t.Fatalf("lost decision result: %s", out)
			}
		})
	}
}

func TestDecisionDoesNotFollowClawRouterCatalogDestination(t *testing.T) {
	var posts atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			posts.Add(1)
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, strings.ReplaceAll(clawRouterCatalogFixture, "/v1/native/openai", "https://foreign.test/keys"))
	}))
	defer h.Close()
	setup := target("clawrouter", model.DecisionOutput, h.URL)
	setup.Model.ID = "openai/gpt-6-luna"
	if _, err := Decide(t.Context(), setup, "evidence", []Question{{"a", "boolean", "Check?", nil}}); err == nil || posts.Load() != 0 {
		t.Fatal("catalog was allowed to change the route or bypass entitlement")
	}
}
