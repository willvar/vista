package generation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vista/internal/model"
)

// These model and route literals come from the provider contracts, not Choices
// or the serializer. Only vary model IDs when the verified schema is identical.
func TestImageProviderContracts(t *testing.T) {
	const workflow = `{"workflow":{"1":{"inputs":{"text":""}},"3":{"inputs":{}}},"promptNode":"1","outputNode":"3"}`
	const promptWorkflow = `{"prompt":{"1":{"inputs":{"text":"A small drawing"}},"3":{"inputs":{}}}}`
	const hosted = `{"model":"gpt-6.1-sol","input":[{"role":"user","content":[{"type":"input_text","text":"A small drawing"}]}],"instructions":"Generate the requested image.","stream":true,"store":false,"tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}],"tool_choice":{"type":"image_generation"}}`
	const completed = "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"status\":\"completed\",\"result\":\"cmVzdWx0\"}]}}\n\n"
	const minimax = `{"model":"image-01","prompt":"A small drawing","n":1,"response_format":"base64"}`
	const minimaxResult = `{"base_resp":{"status_code":0},"data":{"image_base64":["cmVzdWx0"]}}`
	cases := []struct {
		adapter, modelID, base, variant string
		steps                           []wireStep
	}{
		{"xai", "grok-imagine-image", "/v1", "", []wireStep{submit("/v1/images/generations", `{"model":"grok-imagine-image","prompt":"A small drawing","n":1,"response_format":"b64_json"}`, fixtureB64)}},
		{"deepinfra", "black-forest-labs/FLUX-1-schnell", "/v1/openai", "", []wireStep{submit("/v1/openai/images/generations", `{"model":"black-forest-labs/FLUX-1-schnell","prompt":"A small drawing","n":1,"response_format":"b64_json","size":"1024x1024"}`, fixtureB64)}},
		{"litellm", "gpt-image-2", "/v1", "", []wireStep{submit("/v1/images/generations", `{"model":"gpt-image-2","prompt":"A small drawing","n":1,"size":"1024x1024"}`, fixtureB64)}},
		{"clawrouter", "openai/gpt-image-2.5-flare", "", "", []wireStep{
			fetch("/v1/catalog", clawRouterCatalogFixture),
			submit("/v1/images/generations", `{"model":"openai/gpt-image-2.5-flare","prompt":"A small drawing","n":1}`, fixtureB64),
		}},
		{"microsoft-foundry", "image-deployment", "", "MAI", []wireStep{submit("/mai/v1/images/generations", `{"model":"image-deployment","prompt":"A small drawing","width":1024,"height":1024}`, fixtureB64)}},
		{"microsoft-foundry", "other-deployment", "/openai", "GPT-Image", []wireStep{submit("/openai/v1/images/generations", `{"model":"other-deployment","prompt":"A small drawing","n":1}`, fixtureB64)}},
		{"openrouter", "google/gemini-3.1-flash-image-preview", "/api/v1", "", []wireStep{
			submit("/api/v1/images", `{"model":"google/gemini-3.1-flash-image-preview","prompt":"A small drawing","n":1}`, `{"data":[{"b64_json":"cmVzdWx0","media_type":"image/png"}]}`),
		}},
		{"google", "gemini-3.1-flash-image", "", "Gemini", []wireStep{submit("/v1beta/models/gemini-3.1-flash-image:generateContent", `{"contents":[{"parts":[{"text":"A small drawing"}]}],"generationConfig":{"responseModalities":["IMAGE"]}}`, `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"cmVzdWx0"}}]}}]}`)}},
		{"google", "imagen-4.0-generate-001", "/v1beta", "Imagen", []wireStep{submit("/v1beta/models/imagen-4.0-generate-001:predict", `{"instances":[{"prompt":"A small drawing"}],"parameters":{"sampleCount":1}}`, `{"predictions":[{"bytesBase64Encoded":"cmVzdWx0"}]}`)}},
		{"minimax", "image-01", "/anthropic/v1", "", []wireStep{submit("/v1/image_generation", minimax, minimaxResult)}},
		{"minimax-portal", "image-01", "", "", []wireStep{submit("/v1/image_generation", minimax, minimaxResult)}},
		{"vydra", "grok-imagine", "/api/v1", "", []wireStep{
			submit("/api/v1/models/grok-imagine", `{"model":"text-to-image","prompt":"A small drawing"}`, `{"jobId":"job-1","status":"completed","imageUrl":"/asset"}`),
			fetch(fixtureAsset, "result"),
		}},
		{"fal", "fal-ai/flux/dev", "", "", []wireStep{
			submit("/fal-ai/flux/dev", `{"prompt":"A small drawing","num_images":1}`, `{"request_id":"job-1","status_url":"/fal-ai/flux/requests/job-1/status","response_url":"/fal-ai/flux/requests/job-1"}`),
			fetch("/fal-ai/flux/requests/job-1/status", `{"status":"IN_QUEUE"}`),
			fetch("/fal-ai/flux/requests/job-1/status", `{"status":"COMPLETED"}`),
			fetch("/fal-ai/flux/requests/job-1", `{"images":[{"url":"/asset"}]}`), fetch(fixtureAsset, "result"),
		}},
		{"comfy", "workflow", "", "local", []wireStep{
			submit("/prompt", promptWorkflow, `{"prompt_id":"job-1"}`),
			fetch("/history/job-1", `{"job-1":{"outputs":{"3":{"images":[{"filename":"canvas.png","subfolder":"folder with space","type":"output"}]}}}}`),
			fetch("/view?filename=canvas.png&subfolder=folder+with+space&type=output", "result"),
		}},
		{"comfy", "workflow", "/api", "cloud", []wireStep{
			submit("/api/prompt", promptWorkflow, `{"prompt_id":"job-1"}`),
			fetch("/api/jobs/job-1", `{"id":"job-1","status":"pending"}`),
			fetch("/api/jobs/job-1", `{"id":"job-1","status":"completed","outputs":{"3":{"images":[{"filename":"canvas.png"}]}}}`),
			fetch("/api/view?filename=canvas.png&subfolder=&type=output", "result"),
		}},
		{"openai-codex", "gpt-image-2", "/backend-api/codex/responses", "", []wireStep{
			submit("/backend-api/codex/responses", hosted, completed).withHeader("Accept", "text/event-stream").withHeader("OpenAI-Beta", "responses=experimental").withHeader("ChatGPT-Account-Id", "fixture-account"),
		}},
	}
	for _, row := range cases {
		t.Run(row.adapter+"/"+row.variant, func(t *testing.T) {
			base := wireServer(t, fixtureHeaders(row.adapter), row.steps...)
			cfg := wireTarget(row.adapter, model.ImageOutput, base+row.base, row.modelID)
			if row.adapter == "comfy" {
				cfg.Options = json.RawMessage(workflow)
				if row.variant == "cloud" {
					cfg.Options = json.RawMessage(strings.Replace(workflow, `{`, `{"cloud":true,`, 1))
				}
			}
			if row.adapter == "openai-codex" {
				cfg.Connection.ID = "gpt-6.1-sol"
				cfg.Connection.Headers = map[string]string{"ChatGPT-Account-Id": "fixture-account"}
			}
			checkGeneratedFixture(t, cfg, Input{Prompt: fixturePrompt})
		})
	}
}

func TestVideoProviderContracts(t *testing.T) {
	const dashSubmit = "/api/v1/services/aigc/video-generation/video-synthesis"
	const dashPoll = "/api/v1/tasks/job-1"
	dashSteps := func(body, result string) []wireStep {
		return []wireStep{submit(dashSubmit, body, `{"output":{"task_id":"job-1","task_status":"PENDING"}}`).withHeader("X-DashScope-Async", "enable"), fetch(dashPoll, `{"output":{"task_status":"RUNNING"}}`), fetch(dashPoll, result), fetch(fixtureAsset, "result")}
	}
	mmSteps := func(body string) []wireStep {
		return []wireStep{submit("/v1/video_generation", body, `{"task_id":"job-1","base_resp":{"status_code":0}}`), fetch("/v1/query/video_generation?task_id=job-1", `{"status":"Preparing","base_resp":{"status_code":0}}`), fetch("/v1/query/video_generation?task_id=job-1", `{"status":"Success","file_id":"file-1","base_resp":{"status_code":0}}`), fetch("/v1/files/retrieve?file_id=file-1", `{"file":{"download_url":"/asset"},"base_resp":{"status_code":0}}`), fetch(fixtureAsset, "result")}
	}
	novitaSteps := func(id, body string) []wireStep {
		const path = "/v3/async/task-result?task_id=job-1"
		return []wireStep{submit("/v3/async/"+id, body, `{"task_id":"job-1"}`), fetch(path, `{"task":{"status":"TASK_STATUS_PROCESSING"}}`), fetch(path, `{"task":{"status":"TASK_STATUS_SUCCEED"},"videos":[{"video_url":"/asset"}]}`), fetch(fixtureAsset, "result")}
	}
	const submitted = `{"id":"job-1"}`
	const wan = `"duration":5,"audio":false,"prompt_extend":true,"shot_type":"multi","watermark":false`
	cases := []struct {
		adapter, id, base string
		frame             bool
		steps             []wireStep
	}{
		{"xai", "grok-imagine-video", "/v1", true, []wireStep{
			submit("/v1/videos/generations", `{"model":"grok-imagine-video","prompt":"A small drawing","image":{"url":"`+fixtureFrame+`"}}`, `{"request_id":"job-1"}`),
			fetch("/v1/videos/job-1", `{"status":"pending"}`), fetch("/v1/videos/job-1", `{"status":"done","video":{"url":"/asset"}}`), fetch(fixtureAsset, "result"),
		}},
		{"openrouter", "google/veo-3.1-fast", "/api/v1", true, []wireStep{
			submit("/api/v1/videos", `{"model":"google/veo-3.1-fast","prompt":"A small drawing","frame_images":[{"frame_type":"first_frame","type":"image_url","image_url":{"url":"`+fixtureFrame+`"}}]}`, submitted),
			fetch("/api/v1/videos/job-1", `{"status":"completed","unsigned_urls":["/asset"]}`), fetch(fixtureAsset, "result"),
		}},
		{"google", "veo-3.1-fast-generate-preview", "", true, []wireStep{
			submit("/v1beta/models/veo-3.1-fast-generate-preview:predictLongRunning", `{"instances":[{"prompt":"A small drawing","image":{"bytesBase64Encoded":"cG5nLWJ5dGVz","mimeType":"image/png"}}]}`, `{"name":"models/veo-3.1-fast-generate-preview/operations/job-1"}`),
			fetch("/v1beta/models/veo-3.1-fast-generate-preview/operations/job-1", `{"done":false}`),
			fetch("/v1beta/models/veo-3.1-fast-generate-preview/operations/job-1", `{"done":true,"response":{"generateVideoResponse":{"generatedSamples":[{"video":{"uri":"/asset"}}]}}}`), fetch(fixtureAsset, "result"),
		}},
		{"alibaba", "wan2.6-t2v", "/compatible-mode/v1", false, dashSteps(`{"model":"wan2.6-t2v","input":{"prompt":"A small drawing"},"parameters":{"duration":5,"size":"1280*720"}}`, `{"output":{"task_status":"SUCCEEDED","results":[{"video_url":"/asset"}]}}`)},
		{"qwen", "wan2.6-i2v", "/api/v1", true, dashSteps(`{"model":"wan2.6-i2v","input":{"prompt":"A small drawing","img_url":"`+fixtureFrame+`"},"parameters":{"duration":5,"resolution":"720P"}}`, `{"output":{"task_status":"SUCCEEDED","video_url":"/asset"}}`)},
		{"byteplus", "seedance-1-0-pro-250528", "/api/v3", true, []wireStep{
			submit("/api/v3/contents/generations/tasks", `{"model":"seedance-1-0-pro-250528","content":[{"type":"text","text":"A small drawing"},{"type":"image_url","image_url":{"url":"`+fixtureFrame+`"},"role":"first_frame"}]}`, submitted),
			fetch("/api/v3/contents/generations/tasks/job-1", `{"status":"succeeded","content":{"video_url":"/asset"}}`), fetch(fixtureAsset, "result"),
		}},
		{"deepinfra", "Pixverse/Pixverse-T2V", "/v1/openai", false, []wireStep{
			submit("/v1/openai/videos", `{"model":"Pixverse/Pixverse-T2V","prompt":"A small drawing"}`, `{"id":"job-1","status":"succeeded","data":[{"url":"data:video/mp4;base64,cmVzdWx0"}]}`),
		}},
		{"minimax", "MiniMax-Hailuo-2.3", "/anthropic/v1", true, mmSteps(`{"model":"MiniMax-Hailuo-2.3","prompt":"A small drawing","first_frame_image":"` + fixtureFrame + `"}`)},
		{"minimax-portal", "MiniMax-Hailuo-2.3", "", false, mmSteps(`{"model":"MiniMax-Hailuo-2.3","prompt":"A small drawing"}`)},
		{"runway", "gen4.5", "", true, []wireStep{
			submit("/v1/image_to_video", `{"model":"gen4.5","promptText":"A small drawing","promptImage":"`+fixtureFrame+`","ratio":"1280:720","duration":5}`, submitted).withHeader("X-Runway-Version", "2024-11-06"),
			fetch("/v1/tasks/job-1", `{"status":"SUCCEEDED","output":["/asset"]}`).withHeader("X-Runway-Version", "2024-11-06"), fetch(fixtureAsset, "result"),
		}},
		{"runway", "gen4.5", "/v1", false, []wireStep{
			submit("/v1/text_to_video", `{"model":"gen4.5","promptText":"A small drawing","ratio":"1280:720","duration":5}`, submitted).withHeader("X-Runway-Version", "2024-11-06"), fetch("/v1/tasks/job-1", `{"status":"SUCCEEDED","output":["/asset"]}`), fetch(fixtureAsset, "result"),
		}},
		{"together", "Wan-AI/wan2.7-t2v", "/v1", false, []wireStep{
			submit("/v2/videos", `{"model":"Wan-AI/wan2.7-t2v","prompt":"A small drawing"}`, submitted),
			fetch("/v2/videos/job-1", `{"status":"completed","outputs":{"video_url":"/asset"}}`), fetch(fixtureAsset, "result"),
		}},
		{"together", "Wan-AI/wan2.7-i2v", "/v2", true, []wireStep{
			submit("/v2/videos", `{"model":"Wan-AI/wan2.7-i2v","prompt":"A small drawing","media":{"frame_images":[{"input_image":"`+fixtureFrame+`","frame":"first"}]}}`, submitted),
			fetch("/v2/videos/job-1", `{"status":"completed","outputs":[{"video_url":"/asset"}]}`), fetch(fixtureAsset, "result"),
		}},
		{"zai", "cogvideox-3", "/api/paas/v4", true, []wireStep{
			submit("/api/paas/v4/videos/generations", `{"model":"cogvideox-3","prompt":"A small drawing","image_url":"`+fixtureFrame+`"}`, submitted),
			fetch("/api/paas/v4/async-result/job-1", `{"task_status":"SUCCESS","video_result":[{"url":"/asset"}]}`), fetch(fixtureAsset, "result"),
		}},
		{"novita", "wan2.6-t2v", "/openai/v1", false, novitaSteps("wan2.6-t2v", `{"input":{"prompt":"A small drawing"},"parameters":{"size":"1280*720",`+wan+`}}`)},
		{"novita", "wan2.6-i2v", "", true, novitaSteps("wan2.6-i2v", `{"input":{"prompt":"A small drawing","img_url":"`+fixtureFrame+`"},"parameters":{"resolution":"720P",`+wan+`}}`)},
		{"vydra", "veo3", "/api/v1", false, []wireStep{
			submit("/api/v1/models/veo3", `{"prompt":"A small drawing"}`, `{"jobId":"job-1","status":"queued"}`),
			fetch("/api/v1/jobs/job-1", `{"jobId":"job-1","status":"completed","videoUrl":"/asset"}`), fetch(fixtureAsset, "result"),
		}},
		{"pixverse", "v6", "/openapi/v2", true, []wireStep{
			submit("/openapi/v2/image/upload", fixturePNG, `{"ErrCode":0,"Resp":{"img_id":7}}`).withHeader("Ai-Trace-Id", "<new-uuid>"),
			submit("/openapi/v2/video/img/generate", `{"model":"v6","prompt":"A small drawing","duration":5,"quality":"540p","img_id":7,"motion_mode":"normal"}`, `{"ErrCode":0,"Resp":{"video_id":42}}`).withHeader("Ai-Trace-Id", "<new-uuid>"),
			fetch("/openapi/v2/video/result/42", `{"ErrCode":0,"Resp":{"status":5}}`).withHeader("Ai-Trace-Id", "<new-uuid>"),
			fetch("/openapi/v2/video/result/42", `{"ErrCode":0,"Resp":{"status":1,"url":"/asset"}}`).withHeader("Ai-Trace-Id", "<new-uuid>"), fetch(fixtureAsset, "result"),
		}},
		{"fal", "fal-ai/minimax/video-01-live", "", false, []wireStep{
			submit("/fal-ai/minimax/video-01-live", `{"prompt":"A small drawing"}`, `{"request_id":"job-1","status_url":"/fal-ai/minimax/requests/job-1/status","response_url":"/fal-ai/minimax/requests/job-1"}`),
			fetch("/fal-ai/minimax/requests/job-1/status", `{"status":"IN_PROGRESS"}`), fetch("/fal-ai/minimax/requests/job-1/status", `{"status":"COMPLETED"}`),
			fetch("/fal-ai/minimax/requests/job-1", `{"response":{"video":{"url":"/asset"}}}`), fetch(fixtureAsset, "result"),
		}},
		{"comfy", "workflow", "", true, []wireStep{
			submit("/upload/image", fixturePNG, `{"name":"frame.png","subfolder":"","type":"input"}`),
			submit("/prompt", `{"prompt":{"1":{"inputs":{"text":"A small drawing"}},"2":{"inputs":{"image":"frame.png"}},"3":{"inputs":{}}}}`, `{"prompt_id":"job-1"}`),
			fetch("/history/job-1", `{}`), fetch("/history/job-1", `{"job-1":{"outputs":{"3":{"videos":[{"filename":"test.mp4","type":"output"}]}}}}`),
			fetch("/view?filename=test.mp4&subfolder=&type=output", "result"),
		}},
	}
	for _, row := range cases {
		t.Run(row.adapter+"/"+row.id+"/"+row.steps[0].request, func(t *testing.T) {
			base := wireServer(t, fixtureHeaders(row.adapter), row.steps...)
			cfg := wireTarget(row.adapter, model.VideoOutput, base+row.base, row.id)
			if row.adapter == "comfy" {
				cfg.Options = json.RawMessage(`{"workflow":{"1":{"inputs":{"text":""}},"2":{"inputs":{"image":""}},"3":{"inputs":{}}},"promptNode":"1","imageNode":"2","outputNode":"3"}`)
			}
			input := Input{Prompt: fixturePrompt}
			if row.frame {
				input.Frame, input.FrameType = []byte(fixturePNG), "image/png"
			}
			checkGeneratedFixture(t, cfg, input)
		})
	}
}

func TestMediaRejectsProviderFailureWithoutAnotherSubmission(t *testing.T) {
	for _, tc := range []struct {
		adapter, kind, modelID, want string
		steps                        []wireStep
	}{
		{"openai", model.ImageOutput, "gpt-image-2", "HTTP 400", []wireStep{{request: "POST /images/generations", body: `{"model":"gpt-image-2","prompt":"A small drawing","n":1}`, reply: `{"error":{"message":"not for tool output"}}`, status: http.StatusBadRequest}}},
		{"kie", model.VideoOutput, "kling-2.6/text-to-video", "non-success API code", []wireStep{
			submit("/api/v1/jobs/createTask", `{"model":"kling-2.6/text-to-video","input":{"prompt":"A small drawing","sound":false,"duration":"5","aspect_ratio":"16:9"}}`, `{"code":422,"msg":"private diagnostic","data":{"taskId":"do-not-poll"}}`),
		}},
		{"minimax", model.ImageOutput, "image-01", "non-success API code", []wireStep{
			submit("/v1/image_generation", `{"model":"image-01","prompt":"A small drawing","n":1,"response_format":"base64"}`, `{"base_resp":{"status_code":1008,"status_msg":"private diagnostic"},"data":{"image_base64":["cmVzdWx0"]}}`),
		}},
		{"novita", model.VideoOutput, "wan2.6-t2v", "upstream generation failed", []wireStep{
			submit("/v3/async/wan2.6-t2v", `{"input":{"prompt":"A small drawing"},"parameters":{"duration":5,"size":"1280*720","audio":false,"prompt_extend":true,"shot_type":"multi","watermark":false}}`, `{"task_id":"job-1"}`),
			fetch("/v3/async/task-result?task_id=job-1", `{"task":{"status":"TASK_STATUS_FAILED","reason":"private diagnostic"}}`),
		}},
		{"xai", model.VideoOutput, "grok-imagine-video", ErrUpstreamCanceled.Error(), []wireStep{
			submit("/videos/generations", `{"model":"grok-imagine-video","prompt":"A small drawing"}`, `{"request_id":"job-1"}`), fetch("/videos/job-1", `{"status":"cancelled"}`),
		}},
		{"fal", model.VideoOutput, "fal-ai/minimax/video-01-live", "invalid generation task URL", []wireStep{
			submit("/fal-ai/minimax/video-01-live", `{"prompt":"A small drawing"}`, `{"request_id":"job-1","status_url":"https://foreign.invalid/obtain-key","response_url":"/result"}`),
		}},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			base := wireServer(t, fixtureHeaders(tc.adapter), tc.steps...)
			cfg := wireTarget(tc.adapter, tc.kind, base, tc.modelID)
			c, err := newRequest(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			c.wait = time.Millisecond
			asset, err := c.generate(Input{Prompt: fixturePrompt})
			if asset.Body != nil {
				asset.Body.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private diagnostic") {
				t.Fatalf("expected safe failure %q, got %v", tc.want, err)
			}
			if tc.adapter == "xai" && !errors.Is(err, ErrUpstreamCanceled) {
				t.Fatal("lost cancellation identity")
			}
		})
	}
}

func TestInlineDeepInfraVideoUsesMediaBudget(t *testing.T) {
	const size = (2 << 20) + 1024
	data := strings.Repeat("v", size)
	url := "data:video/mp4;base64," + base64.StdEncoding.EncodeToString([]byte(data))
	base := wireServer(t, fixtureHeaders("deepinfra"), submit("/v1/openai/videos", `{"model":"Pixverse/Pixverse-T2V","prompt":"A small drawing"}`, `{"id":"job-1","status":"succeeded","data":[{"url":"`+url+`"}]}`))
	out, err := Generate(t.Context(), wireTarget("deepinfra", model.VideoOutput, base+"/v1/openai", "Pixverse/Pixverse-T2V"), Input{Prompt: fixturePrompt})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil || string(b) != data {
		t.Fatalf("inline output length %d, err=%v", len(b), err)
	}
}

func TestMismatchedMediaModelOrModeDoesNotSubmit(t *testing.T) {
	for _, tc := range []struct {
		adapter, id string
		frame       bool
	}{
		{"google", "gemini-omni-flash-preview", false},
		{"openrouter", "black-forest-labs/flux-video-upscale", false},
		{"kie", "kling-2.6/image-to-video", false},
		{"vydra", "kling", false},
		{"fal", "fal-ai/minimax/video-01-live", true},
		{"alibaba", "wan2.6-i2v", false},
		{"novita", "wan2.6-t2v", true},
		{"minimax", "MiniMax-Hailuo-2.3-Fast", false},
		{"together", "Wan-AI/wan2.7-i2v", false},
	} {
		t.Run(tc.adapter+"/"+tc.id, func(t *testing.T) {
			base := wireServer(t, fixtureHeaders(tc.adapter))
			cfg := wireTarget(tc.adapter, model.VideoOutput, base, tc.id)
			in := Input{Prompt: fixturePrompt}
			if tc.frame {
				in.Frame, in.FrameType = []byte(fixturePNG), "image/png"
			}
			if a, err := Generate(t.Context(), cfg, in); err == nil {
				a.Body.Close()
				t.Fatal("unsupported request succeeded")
			}
		})
	}
}

func TestAssetRedirectDoesNotFollowAnotherLoopbackOrigin(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed foreign loopback redirect") }))
	defer other.Close()
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			io.WriteString(w, `{"data":[{"url":"/asset"}]}`)
			return
		}
		http.Redirect(w, r, other.URL+"/asset", http.StatusFound)
	}))
	defer service.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if a, err := Generate(ctx, wireTarget("openai", model.ImageOutput, service.URL, "gpt-image-2"), Input{Prompt: fixturePrompt}); err == nil {
		a.Body.Close()
		t.Fatal("foreign loopback redirect succeeded")
	}
}
