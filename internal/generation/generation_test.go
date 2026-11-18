package generation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"vista/internal/model"
	"vista/internal/network"
)

func target(id, kind, base string) model.GenerationTarget {
	return model.GenerationTarget{Kind: kind, Model: model.Model{ProviderID: id, BaseURL: base, APIKey: "fixture-key", ID: "fixture-model", Network: &network.Policy{}}}
}

func TestImageProtocolFixtures(t *testing.T) {
	cases := []struct{ id, model, path, reply string }{
		{"openai", "gpt-image-2", "/images/generations", `{"data":[{"b64_json":"cmVzdWx0"}]}`},
		{"xai", "grok-imagine-image", "/images/generations", `{"data":[{"b64_json":"cmVzdWx0"}]}`},
		{"deepinfra", "bfl/flux", "/images/generations", `{"data":[{"b64_json":"cmVzdWx0"}]}`},
		{"litellm", "configured", "/images/generations", `{"data":[{"b64_json":"cmVzdWx0"}]}`},
		{"clawrouter", "openai/gpt-image-2.5-flare", "/images/generations", `{"data":[{"b64_json":"cmVzdWx0"}]}`},
		{"microsoft-foundry", "mai-image", "/images/generations", `{"data":[{"b64_json":"cmVzdWx0"}]}`},
		{"openrouter", "vendor/image", "/images", `{"data":[{"b64_json":"cmVzdWx0"}]}`},
		{"google", "gemini-3.1-flash-image", "/models/gemini-3.1-flash-image:generateContent", `{"candidates":[{"content":{"parts":[{"inlineData":{"data":"cmVzdWx0"}}]}}]}`},
		{"google", "imagen-4.0-generate-001", "/models/imagen-4.0-generate-001:predict", `{"predictions":[{"bytesBase64Encoded":"cmVzdWx0"}]}`},
		{"minimax", "image-01", "/v1/image_generation", `{"data":{"image_base64":["cmVzdWx0"]}}`},
		{"minimax-portal", "image-01", "/v1/image_generation", `{"data":{"image_base64":["cmVzdWx0"]}}`},
		{"vydra", "grok-imagine", "/models/grok-imagine", `{"jobId":"1","status":"completed","imageUrl":"/asset"}`},
	}
	for _, tc := range cases {
		t.Run(tc.id+"-"+tc.model, func(t *testing.T) {
			var posts int
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/asset" && r.Method == http.MethodGet {
					io.WriteString(w, "result")
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != tc.path {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				posts++
				var raw json.RawMessage
				if json.NewDecoder(r.Body).Decode(&raw) != nil || !strings.Contains(string(raw), "small drawing") {
					t.Error("description missing from request")
				}
				io.WriteString(w, tc.reply)
			}))
			defer h.Close()
			req := target(tc.id, model.ImageOutput, h.URL)
			req.Model.ID = tc.model
			asset, err := Generate(t.Context(), req, Input{Prompt: "small drawing"})
			if err != nil {
				t.Fatal(err)
			}
			defer asset.Body.Close()
			data, e := io.ReadAll(asset.Body)
			if e != nil || string(data) != "result" || posts != 1 {
				t.Fatalf("asset=%q posts=%d error=%v", data, posts, e)
			}
		})
	}
}

func TestVideoProtocolFixtures(t *testing.T) {
	cases := []struct {
		id, path, poll, initial, result string
		frame                           bool
	}{
		{"xai", "/videos/generations", "/videos/task-a", `{"request_id":"task-a"}`, `{"status":"done","video":{"url":"/asset"}}`, true},
		{"openrouter", "/videos", "/videos/task-a", `{"id":"task-a"}`, `{"status":"completed","unsigned_urls":["/asset"]}`, true},
		{"google", "/models/fixture-model:predictLongRunning", "/models/fixture-model/operations/task-a", `{"name":"models/fixture-model/operations/task-a"}`, `{"done":true,"response":{"generateVideoResponse":{"generatedSamples":[{"video":{"uri":"/asset"}}]}}}`, true},
		{"alibaba", "/api/v1/services/aigc/video-generation/video-synthesis", "/api/v1/tasks/task-a", `{"output":{"task_id":"task-a"}}`, `{"output":{"task_status":"SUCCEEDED","video_url":"/asset"}}`, true},
		{"qwen", "/api/v1/services/aigc/video-generation/video-synthesis", "/api/v1/tasks/task-a", `{"output":{"task_id":"task-a"}}`, `{"output":{"task_status":"SUCCEEDED","video_url":"/asset"}}`, true},
		{"byteplus", "/contents/generations/tasks", "/contents/generations/tasks/task-a", `{"id":"task-a"}`, `{"status":"succeeded","content":{"video_url":"/asset"}}`, true},
		{"deepinfra", "/videos", "/videos/task-a", `{"id":"task-a"}`, `{"status":"succeeded","data":[{"url":"/asset"}]}`, false},
		{"minimax", "/v1/video_generation", "/v1/query/video_generation", `{"task_id":"task-a"}`, `{"status":"Success","video_url":"/asset"}`, true},
		{"minimax-portal", "/v1/video_generation", "/v1/query/video_generation", `{"task_id":"task-a"}`, `{"status":"Success","video_url":"/asset"}`, true},
		{"runway", "/v1/image_to_video", "/v1/tasks/task-a", `{"id":"task-a"}`, `{"status":"SUCCEEDED","output":["/asset"]}`, true},
		{"together", "/v2/videos", "/v2/videos/task-a", `{"id":"task-a"}`, `{"status":"completed","outputs":[{"video_url":"/asset"}]}`, true},
		{"zai", "/videos/generations", "/async-result/task-a", `{"id":"task-a"}`, `{"task_status":"SUCCESS","video_result":[{"url":"/asset"}]}`, true},
		{"novita", "/v3/async/fixture-model", "/v3/async/task-result", `{"task_id":"task-a"}`, `{"task":{"status":"TASK_STATUS_SUCCEED"},"videos":[{"video_url":"/asset"}]}`, true},
		{"vydra", "/models/fixture-model", "/jobs/task-a", `{"jobId":"task-a","status":"queued"}`, `{"status":"completed","videoUrl":"/asset"}`, false},
		{"kie", "/api/v1/jobs/createTask", "/api/v1/jobs/recordInfo", `{"data":{"taskId":"task-a"}}`, `{"data":{"state":"success","resultJson":"{\"resultUrls\":[\"/asset\"]}"}}`, false},
		{"pixverse", "/video/img/generate", "/video/result/42", `{"ErrCode":0,"Resp":{"video_id":42}}`, `{"ErrCode":0,"Resp":{"status":1,"url":"/asset"}}`, true},
		{"fal", "/fixture-model", "/request/status", `{"status_url":"/request/status","response_url":"/request/result"}`, `{"status":"COMPLETED"}`, true},
		{"comfy", "/prompt", "/history/task-a", `{"prompt_id":"task-a"}`, `{"task-a":{"outputs":{"3":{"videos":[{"filename":"test.mp4","type":"output"}]}}}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			var mu sync.Mutex
			var routes []string
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				routes = append(routes, r.Method+" "+r.URL.Path)
				mu.Unlock()
				switch r.URL.Path {
				case tc.path:
					var raw json.RawMessage
					if json.NewDecoder(r.Body).Decode(&raw) != nil || r.Method != http.MethodPost {
						t.Error("invalid submit")
					}
					if tc.id != "comfy" && tc.frame && tc.id != "pixverse" && !strings.Contains(string(raw), "cG5nLWJ5dGVz") {
						t.Error("first frame bytes missing")
					}
					io.WriteString(w, tc.initial)
				case tc.poll:
					io.WriteString(w, tc.result)
				case "/upload/image":
					io.WriteString(w, `{"name":"fixture.png"}`)
				case "/image/upload":
					io.WriteString(w, `{"Resp":{"img_id":7}}`)
				case "/request/result":
					io.WriteString(w, `{"video":{"url":"/asset"}}`)
				case "/asset", "/view":
					io.WriteString(w, "bytes")
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer h.Close()
			setup := target(tc.id, model.VideoOutput, h.URL)
			if tc.id == "comfy" {
				setup.Options = json.RawMessage(`{"workflow":{"1":{"inputs":{"text":""}},"2":{"inputs":{"image":""}},"3":{"inputs":{}}},"promptNode":"1","imageNode":"2","outputNode":"3"}`)
			}
			req, err := newRequest(t.Context(), setup)
			if err != nil {
				t.Fatal(err)
			}
			req.wait = time.Millisecond
			input := Input{Prompt: "short neutral test"}
			if tc.frame {
				input.Frame = []byte("png-bytes")
				input.FrameType = "image/png"
			}
			asset, err := req.video(input)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(asset.Body)
			asset.Body.Close()
			if err != nil || string(data) != "bytes" {
				t.Fatalf("media=%q error=%v", data, err)
			}
			mu.Lock()
			defer mu.Unlock()
			submits := 0
			for _, route := range routes {
				if route == "POST "+tc.path {
					submits++
				}
			}
			if submits != 1 || !slices.Contains(routes, "GET "+tc.poll) {
				t.Fatalf("incorrect task lifecycle: %v", routes)
			}
		})
	}
}

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

func TestImageSpecializedProtocols(t *testing.T) {
	for _, id := range []string{"fal", "comfy", "openai-codex"} {
		t.Run(id, func(t *testing.T) {
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/fixture-model":
					io.WriteString(w, `{"status_url":"/status","response_url":"/result"}`)
				case "/status":
					io.WriteString(w, `{"status":"COMPLETED"}`)
				case "/result":
					io.WriteString(w, `{"images":[{"url":"/asset"}]}`)
				case "/prompt":
					io.WriteString(w, `{"prompt_id":"task"}`)
				case "/history/task":
					io.WriteString(w, `{"task":{"outputs":{"3":{"images":[{"filename":"test.png","type":"output"}]}}}}`)
				case "/asset", "/view":
					io.WriteString(w, "result")
				case "/responses":
					if r.Method != http.MethodPost || r.Header.Get("Accept") != "text/event-stream" {
						t.Error("invalid hosted request")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"cmVzdWx0\"}}\n\ndata: {\"type\":\"response.completed\"}\n\n")
				default:
					http.NotFound(w, r)
				}
			}))
			defer h.Close()
			setup := target(id, model.ImageOutput, h.URL)
			if id == "comfy" {
				setup.Options = json.RawMessage(`{"workflow":{"1":{"inputs":{"text":""}},"3":{"inputs":{}}},"promptNode":"1","outputNode":"3"}`)
			}
			if id == "openai-codex" {
				source := setup.Model
				source.ID = "chat-carrier"
				setup.Model.BaseURL, setup.Model.APIKey = "", ""
				setup.Connection = &source
			}
			req, err := newRequest(t.Context(), setup)
			if err != nil {
				t.Fatal(err)
			}
			req.wait = time.Millisecond
			asset, err := req.image("neutral prompt")
			if err != nil {
				t.Fatal(err)
			}
			defer asset.Body.Close()
			b, e := io.ReadAll(asset.Body)
			if e != nil || string(b) != "result" {
				t.Fatalf("media=%q error=%v", b, e)
			}
		})
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
