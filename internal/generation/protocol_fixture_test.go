package generation

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"vista/internal/model"
)

const (
	fixturePrompt = "A small drawing"
	fixturePNG    = "png-bytes"
	fixtureFrame  = "data:image/png;base64,cG5nLWJ5dGVz"
	fixtureB64    = `{"data":[{"b64_json":"cmVzdWx0"}]}`
	fixtureAsset  = "/asset"
)

// A strict provider substitute: compare the full request before releasing the
// documented reply, including the query, credentials, payload and call order.
// Do not derive fixtures from the production encoder or its route table.
type wireStep struct {
	request, body, reply string
	headers              map[string]string
	status               int
}

func submit(path, body, reply string) wireStep {
	return wireStep{request: "POST " + path, body: body, reply: reply}
}
func fetch(path, reply string) wireStep { return wireStep{request: "GET " + path, reply: reply} }
func (s wireStep) withHeader(name, value string) wireStep {
	if s.headers == nil {
		s.headers = map[string]string{}
	}
	s.headers[name] = value
	return s
}

func wireServer(t *testing.T, headers map[string]string, steps ...wireStep) string {
	t.Helper()
	var mu sync.Mutex
	next := 0
	traceIDs := map[string]bool{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got := r.Method + " " + r.URL.RequestURI()
		if next >= len(steps) || steps[next].request != got {
			t.Errorf("unexpected exchange %d: %s", next+1, got)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		step := steps[next]
		next++
		for _, key := range []string{"Authorization", "x-goog-api-key", "api-key", "x-api-key", "Idempotency-Key", "X-Idempotency-Key"} {
			if r.Header.Get(key) != headers[key] {
				t.Errorf("%s: unexpected %s header", got, key)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		for key, want := range headers {
			if r.Header.Get(key) != want {
				t.Errorf("%s: wrong %s header", got, key)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		for key, want := range step.headers {
			got := r.Header.Get(key)
			if want == "<new-uuid>" {
				if _, err := uuid.Parse(got); err == nil && !traceIDs[got] {
					traceIDs[got] = true
					continue
				}
			} else if got == want {
				continue
			}
			t.Errorf("invalid or missing step header %s on %s", key, step.request)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if step.body == fixturePNG {
			f, filename, err := r.FormFile("image")
			if err != nil {
				t.Errorf("%s: missing multipart image: %v", got, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer r.MultipartForm.RemoveAll()
			defer f.Close()
			payload, err := io.ReadAll(f)
			if err != nil || filename.Filename != "frame.png" || filename.Header.Get("Content-Type") != "image/png" || string(payload) != fixturePNG {
				t.Errorf("%s: wrong multipart image", got)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		} else {
			payload, err := io.ReadAll(r.Body)
			if err != nil || (step.body == "" && len(payload) != 0) || (step.body != "" && (r.Header.Get("Content-Type") != "application/json" || !sameFixtureJSON(payload, step.body))) {
				t.Errorf("%s: wrong request body\n got %s\nwant %s", got, payload, step.body)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		if strings.HasPrefix(step.reply, "data: ") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if step.status != 0 {
			w.WriteHeader(step.status)
		}
		io.WriteString(w, step.reply)
	}))
	t.Cleanup(func() {
		s.Close()
		if next != len(steps) {
			t.Errorf("completed %d of %d expected exchanges", next, len(steps))
		}
	})
	return s.URL
}

func sameFixtureJSON(got []byte, want string) bool {
	decode := func(data []byte) (any, bool) {
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		var v any
		ok := d.Decode(&v) == nil && d.Decode(new(any)) == io.EOF
		return v, ok
	}
	a, okA := decode(got)
	b, okB := decode([]byte(want))
	return okA && okB && reflect.DeepEqual(a, b)
}

func wireTarget(adapter, kind, base, id string) model.GenerationTarget {
	t := target(adapter, kind, base)
	source := t.Model
	t.Model.ID = id
	t.Connection = &source
	return t
}

func fixtureHeaders(adapter string) map[string]string {
	switch adapter {
	case "google":
		return map[string]string{"x-goog-api-key": "fixture-key"}
	case "microsoft-foundry", "pixverse":
		return map[string]string{"api-key": "fixture-key"}
	case "fal":
		return map[string]string{"Authorization": "Key fixture-key"}
	case "comfy":
		return map[string]string{"x-api-key": "fixture-key"}
	case "onnx":
		return nil
	default:
		return map[string]string{"Authorization": "Bearer fixture-key"}
	}
}

func checkGeneratedFixture(t *testing.T, target model.GenerationTarget, input Input) {
	t.Helper()
	c, err := newRequest(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	c.wait = time.Millisecond
	a, err := c.generate(input)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Body.Close()
	data, err := io.ReadAll(a.Body)
	if err != nil || string(data) != "result" {
		t.Fatalf("unexpected original: %q, %v", data, err)
	}
}

func TestStrictOpenAIImageWire(t *testing.T) {
	for _, id := range []string{"gpt-image-2", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "gpt-image-1.5", "gpt-image-1-mini"} {
		t.Run(id, func(t *testing.T) {
			base := wireServer(t, map[string]string{"Authorization": "Bearer fixture-key", "OpenAI-Project": "fixture-project"}, submit("/v1/images/generations", `{"model":"`+id+`","prompt":"A small drawing","n":1}`, fixtureB64))
			target := target("openai", model.ImageOutput, base+"/v1")
			target.Model.ID, target.Model.OpenAIProject = id, "fixture-project"
			checkGeneratedFixture(t, target, Input{Prompt: fixturePrompt})
		})
	}
}

func TestStrictKlingVideoWire(t *testing.T) {
	base := wireServer(t, map[string]string{"Authorization": "Bearer fixture-key"},
		submit("/api/v1/jobs/createTask", `{"model":"kling-2.6/text-to-video","input":{"prompt":"A small drawing","sound":false,"duration":"5","aspect_ratio":"16:9"}}`, `{"code":200,"msg":"success","data":{"taskId":"task-a"}}`),
		fetch("/api/v1/jobs/recordInfo?taskId=task-a", `{"code":200,"data":{"state":"waiting","resultJson":""}}`),
		fetch("/api/v1/jobs/recordInfo?taskId=task-a", `{"code":200,"data":{"state":"success","resultJson":"{\"resultUrls\":[\"/asset\"]}"}}`),
		fetch(fixtureAsset, "result"),
	)
	target := target("kie", model.VideoOutput, base)
	target.Model.ID = "kling-2.6/text-to-video"
	checkGeneratedFixture(t, target, Input{Prompt: fixturePrompt})
}
