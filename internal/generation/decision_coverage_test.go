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
	const systemQuestions = `{"verify":{"type":"noul","instructions":"Check?"},"color":{"type":"choice","instructions":"Which?","criteria":{"red":"red","blue":"blue"}},"quality":{"type":"score","instructions":"Quality?","criteria":["poor","good"]}}`
	const nativeQuestions = `[{"name":"verify","type":"predicate","instructions":"Check?"},{"name":"color","type":"choice","instructions":"Which?","choices":[{"value":"red","description":"red"},{"value":"blue","description":"blue"}]},{"name":"quality","type":"score","instructions":"Quality?","levels":[{"label":"0","description":"poor"},{"label":"1","description":"good"}]}]`
	const sageQuestions = `[{"id":"verify","kind":"yesno","instructions":"Check?"},{"id":"color","kind":"choice","instructions":"Which?","options":[{"option":"red","description":"red"},{"option":"blue","description":"blue"}]},{"id":"quality","kind":"scale","instructions":"Quality?","levels":[{"level":0,"description":"poor"},{"level":1,"description":"good"}]}]`
	const answers = `"answers":{"verify":{"type":"noul","noul":0.8},"color":{"type":"choice","choice":"red"},"quality":{"type":"score","score":0.75,"legend":{"0":"poor","1":"good"}}}`
	const openai = `{"model":"gpt-6-luna","answers":[{"name":"verify","type":"predicate","probability":0.8},{"name":"color","type":"choice","choice":"red","confidence":0.75,"probabilities":[{"value":"red","probability":0.75},{"value":"blue","probability":0.25}]},{"name":"quality","type":"score","score":0.75,"confidence":0.75,"probabilities":[{"label":"0","value":0,"probability":0.25},{"label":"1","value":1,"probability":0.75}]}]}`
	const sage = `{"results":[{"answers":[{"ok":true,"result":{"id":"verify","kind":"yesno","result":{"answer":"yes","probability":0.8}}},{"ok":true,"result":{"id":"color","kind":"choice","result":{"chosen":"red"}}},{"ok":true,"result":{"id":"quality","kind":"scale","result":{"expectation":0.75}}}]}],"meta":{"model":"levanto-sage"}}`
	questions := []Question{{"verify", "boolean", "Check?", nil}, {"color", "choice", "Which?", []string{"red", "blue"}}, {"quality", "score", "Quality?", []string{"poor", "good"}}}
	for _, row := range []struct{ adapter, id, base, path string }{
		{"openai", "gpt-6-luna", "/v1", "/v1/decisions"},
		{"clawrouter", "openai/gpt-6-luna", "/v1", "/v1/native/openai/v1/decisions"},
		{"google", "gemini-decision-1", "/v1beta", "/v1beta/decisions"},
		{"levanto", "levanto-sage", "", "/decide/batch"},
		{"openrouter", "typesafe/jev-1.14", "/api/v1", "/api/v1/systemone"},
		{"onnx", "fixture-local-classifier", "/v1", "/v1/systemone"},
		{"opencode", "jev-1.13", "/inference/systemone/v1", "/inference/systemone/v1/systemone"},
		{"typesafe", "jev-latest", "/v1", "/v1/systemone"},
		{"liquid-ai", "d1-ultra", "/decisions/v1", "/decisions/v1/systemone"},
		{"baseten", "decision-models/baseten-elephant-9b-latest", "/v1", "/v1/systemone"},
		{"subq", "subq-snap-preview", "/v1", "/v1/systemone"},
	} {
		t.Run(row.adapter+"/"+row.id, func(t *testing.T) {
			modelID := `"` + row.id + `"`
			body := `{"model":` + modelID + `,"state":"short evidence","questions":` + systemQuestions + `}`
			reply := `{"model":` + modelID + `,` + answers + `,"usage":{"input_tokens":4,"output_tokens":3}}`
			var steps []wireStep
			switch row.adapter {
			case "clawrouter":
				modelID = `"gpt-6-luna"`
				steps = append(steps, fetch("/v1/catalog", clawRouterCatalogFixture))
				fallthrough
			case "openai":
				body = `{"model":` + modelID + `,"input":"short evidence","questions":` + nativeQuestions + `}`
				reply = openai
			case "google":
				body = `{"model":` + modelID + `,"input":"short evidence","questions":` + systemQuestions + `}`
				reply = `{` + answers + `,"usage":{"total_input_tokens":4,"total_output_tokens":0}}`
			case "levanto":
				body = `{"requests":[{"content":"short evidence","questions":` + sageQuestions + `}]}`
				reply = sage
			}
			steps = append(steps, submit(row.path, body, reply))
			base := wireServer(t, fixtureHeaders(row.adapter), steps...)
			cfg := wireTarget(row.adapter, model.DecisionOutput, base+row.base, row.id)
			if row.adapter == "onnx" {
				cfg.Connection.APIKey = ""
			}
			result, err := Decide(t.Context(), cfg, "short evidence", questions)
			if err != nil {
				t.Fatal(err)
			}
			var decoded wire
			d := json.NewDecoder(strings.NewReader(string(result)))
			d.UseNumber()
			if err := d.Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			if getValue(decoded, "answers", "verify", "noul") != json.Number("0.8") || text(decoded, "answers", "color", "choice") != "red" || getValue(decoded, "answers", "quality", "score") != json.Number("0.75") {
				t.Fatalf("lost or changed decision result: %s", result)
			}
		})
	}
}

func TestNativeDecisionRefusalAndAbstentionAreNotNegativeAnswers(t *testing.T) {
	for _, row := range []struct{ adapter, id, path, body, reply, kind string }{
		{"openai", "gpt-6-luna", "/decisions", `{"model":"gpt-6-luna","input":"evidence","questions":[{"name":"a","type":"predicate","instructions":"Check?"}]}`, `{"model":"gpt-6-luna","answers":[{"name":"a","type":"refusal"}]}`, "error"},
		{"levanto", "levanto-sage", "/decide", `{"content":"evidence","question":{"id":"a","kind":"yesno","instructions":"Check?"}}`, `{"id":"a","kind":"yesno","result":{"answer":null,"probability":0.5}}`, "abstained"},
	} {
		t.Run(row.adapter, func(t *testing.T) {
			base := wireServer(t, fixtureHeaders(row.adapter), submit(row.path, row.body, row.reply))
			result, err := Decide(t.Context(), wireTarget(row.adapter, model.DecisionOutput, base, row.id), "evidence", []Question{{"a", "boolean", "Check?", nil}})
			if err != nil {
				t.Fatal(err)
			}
			var decoded wire
			if json.Unmarshal(result, &decoded) != nil || text(decoded, "answers", "a", "type") != row.kind || getValue(decoded, "answers", "a", "noul") != nil {
				t.Fatalf("native rejection was not preserved: %s", result)
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
