package generation

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"vista/internal/model"
)

type Input struct {
	Prompt    string
	Frame     []byte
	FrameType string
}

func Generate(ctx context.Context, target model.GenerationTarget, input Input) (Asset, error) {
	c, err := newRequest(ctx, target)
	if err != nil {
		return Asset{}, err
	}
	spec, _ := Find(target.Model.ProviderID, target.Kind)
	if strings.TrimSpace(input.Prompt) == "" {
		return Asset{}, errors.New("a generation description is required")
	}
	if len(input.Frame) > 0 && (!spec.Frame || target.Kind != model.VideoOutput || (len(target.InputModalities) > 0 && !slices.Contains(target.InputModalities, "image"))) {
		return Asset{}, errors.New("this adapter cannot use a private first frame")
	}
	if target.Kind == model.ImageOutput {
		return c.image(input.Prompt)
	}
	if target.Kind == model.VideoOutput {
		return c.video(input)
	}
	return Asset{}, errors.New("generation operation does not produce media")
}

func (i Input) dataURL() string {
	if len(i.Frame) == 0 {
		return ""
	}
	return "data:" + i.FrameType + ";base64," + base64.StdEncoding.EncodeToString(i.Frame)
}

func (c request) image(prompt string) (Asset, error) {
	body := wire{"model": c.model, "prompt": prompt, "n": 1, "response_format": "b64_json"}
	path := "images/generations"
	var extra http.Header
	switch c.target.Model.ProviderID {
	case "openrouter":
		path = "images"
		delete(body, "response_format")
	case "clawrouter":
		delete(body, "response_format")
	case "minimax", "minimax-portal":
		path = "v1/image_generation"
		delete(body, "response_format")
		body["response_format"] = "base64"
	case "google":
		if strings.HasPrefix(c.model, "imagen-") {
			path = "models/" + c.model + ":predict"
			body = wire{"instances": []wire{{"prompt": prompt}}, "parameters": wire{"sampleCount": 1}}
		} else {
			path = "models/" + c.model + ":generateContent"
			body = wire{"contents": []wire{{"parts": []wire{{"text": prompt}}}}, "generationConfig": wire{"responseModalities": []string{"IMAGE"}}}
		}
	case "fal":
		return c.fal(Input{Prompt: prompt}, false)
	case "comfy":
		return c.comfy(Input{Prompt: prompt}, false)
	case "openai-codex":
		return c.hostedImage(prompt)
	case "vydra":
		return c.vydra(Input{Prompt: prompt}, false)
	case "microsoft-foundry":
		delete(body, "response_format")
	}
	out, err := c.api(http.MethodPost, c.route(path), body, extra)
	if err != nil {
		return Asset{}, err
	}
	return c.asset(first(text(out, "data", 0, "url"), text(out, "data", "image_urls", 0)), first(text(out, "data", 0, "b64_json"), text(out, "data", "image_base64", 0), text(out, "predictions", 0, "bytesBase64Encoded"), inlineImage(out)))
}

func inlineImage(out wire) string {
	parts, _ := getValue(out, "candidates", 0, "content", "parts").([]any)
	for _, part := range parts {
		if data := first(text(part, "inlineData", "data"), text(part, "inline_data", "data")); data != "" {
			return data
		}
	}
	return ""
}

func (c request) hostedImage(prompt string) (Asset, error) {
	// Codex authorization and transport identity are taken from the chosen
	// connection. The outer text model carries the hosted image tool.
	if c.target.Connection == nil {
		return Asset{}, errors.New("an authorized Codex connection is required")
	}
	body := wire{"model": c.target.Connection.ID, "input": []wire{{"role": "user", "content": prompt}}, "instructions": "Generate the requested image.", "stream": true, "store": false, "tools": []wire{{"type": "image_generation", "model": c.model}}, "tool_choice": wire{"type": "image_generation"}}
	return c.responseImage(body)
}

func (c request) video(input Input) (Asset, error) {
	frame := input.dataURL()
	body := wire{"model": c.model, "prompt": input.Prompt}
	p := c.target.Model.ProviderID
	if p == "fal" {
		return c.fal(input, true)
	}
	if p == "comfy" {
		return c.comfy(input, true)
	}
	if p == "pixverse" {
		return c.pixverse(input)
	}
	if p == "kie" {
		return c.kie(input)
	}
	if p == "vydra" {
		return c.vydra(input, true)
	}
	if p == "novita" {
		return c.novita(input)
	}
	var path, idField string
	var extra http.Header
	switch p {
	case "xai":
		path, idField = "videos/generations", "request_id"
		if frame != "" {
			body["image"] = wire{"url": frame}
		}
	case "openrouter":
		path, idField = "videos", "id"
		if frame != "" {
			body["frame_images"] = []wire{{"frame_type": "first_frame", "type": "image_url", "image_url": wire{"url": frame}}}
		}
	case "google":
		path, idField = "models/"+c.model+":predictLongRunning", "name"
		instance := wire{"prompt": input.Prompt}
		if frame != "" {
			instance["image"] = wire{"bytesBase64Encoded": base64.StdEncoding.EncodeToString(input.Frame), "mimeType": input.FrameType}
		}
		body = wire{"instances": []wire{instance}}
	case "alibaba", "qwen":
		path, idField = "api/v1/services/aigc/video-generation/video-synthesis", "task_id"
		body = wire{"model": c.model, "input": wire{"prompt": input.Prompt}}
		if frame != "" {
			body["input"].(wire)["img_url"] = frame
		}
		extra = http.Header{"X-DashScope-Async": {"enable"}}
	case "byteplus":
		path, idField = "contents/generations/tasks", "id"
		content := []wire{{"type": "text", "text": input.Prompt}}
		if frame != "" {
			content = append(content, wire{"type": "image_url", "image_url": wire{"url": frame}, "role": "first_frame"})
		}
		body = wire{"model": c.model, "content": content}
	case "deepinfra":
		path, idField = "videos", "id"
	case "minimax", "minimax-portal":
		path, idField = "v1/video_generation", "task_id"
		if frame != "" {
			body["first_frame_image"] = frame
		}
	case "runway":
		path, idField = "v1/text_to_video", "id"
		body = wire{"model": c.model, "promptText": input.Prompt, "ratio": "1280:720", "duration": 5}
		if frame != "" {
			path = "v1/image_to_video"
			body["promptImage"] = frame
		}
		extra = http.Header{"X-Runway-Version": {"2024-11-06"}}
	case "together":
		path, idField = "v2/videos", "id"
		if frame != "" {
			body["media"] = wire{"frame_images": []wire{{"input_image": frame, "frame": "first"}}}
		}
	case "zai":
		path, idField = "videos/generations", "id"
		if frame != "" {
			body["image_url"] = frame
		}
	default:
		return Asset{}, errors.New("no video wire format for the selected adapter")
	}
	out, err := c.api(http.MethodPost, c.route(path), body, extra)
	if err != nil {
		return Asset{}, err
	}
	id := first(jobID(out, idField), jobID(out, "output", idField))
	if p == "google" {
		return c.veoResult(id)
	}
	if !validTaskID(id) {
		return Asset{}, errors.New("video submission did not return a valid task ID; do not resubmit automatically")
	}
	subPath := "videos/" + id
	switch p {
	case "alibaba", "qwen":
		subPath = "api/v1/tasks/" + id
	case "byteplus":
		subPath = path + "/" + id
	case "deepinfra":
		subPath = path + "/" + id
	case "minimax", "minimax-portal":
		subPath = "v1/query/video_generation?task_id=" + url.QueryEscape(id)
	case "runway":
		subPath = "v1/tasks/" + id
	case "together":
		subPath = path + "/" + id
	case "zai":
		subPath = "async-result/" + id
	}
	result, err := c.pollWithHeaders(c.route(subPath), extra, func(out wire) (bool, error) {
		return status(first(text(out, "status"), text(out, "task_status"), text(out, "output", "task_status")))
	})
	if err != nil {
		return Asset{}, err
	}
	raw := first(text(result, "video", "url"), text(result, "content", "video_url"), text(result, "output", "video_url"), text(result, "output", 0), text(result, "video_url"), text(result, "data", 0, "url"), text(result, "video_result", 0, "url"), text(result, "outputs", "video_url"), text(result, "outputs", 0, "video_url"), text(result, "outputs", 0, "url"), text(result, "data", 0, "video_url"))
	if p == "openrouter" {
		raw = first(text(result, "unsigned_urls", 0), text(result, "urls", 0), c.route(subPath+"/content"))
	}
	if (p == "minimax" || p == "minimax-portal") && raw == "" {
		file := jobID(result, "file_id")
		if !validTaskID(file) {
			return Asset{}, errors.New("completed video has no file ID")
		}
		out, e := c.api(http.MethodGet, c.route("v1/files/retrieve?file_id="+url.QueryEscape(file)), nil, nil)
		if e != nil {
			return Asset{}, e
		}
		raw = text(out, "file", "download_url")
	}
	return c.asset(raw, "")
}

func (c request) veoResult(name string) (Asset, error) {
	if !strings.Contains(name, "/operations/") && !strings.HasPrefix(name, "operations/") {
		return Asset{}, errors.New("invalid Veo operation name")
	}
	for _, part := range strings.Split(name, "/") {
		if !validTaskID(part) {
			return Asset{}, errors.New("invalid Veo operation name")
		}
	}
	out, err := c.poll(c.route(name), func(out wire) (bool, error) { done, _ := out["done"].(bool); return done, nil })
	if err != nil {
		return Asset{}, err
	}
	video := getValue(out, "response", "generateVideoResponse", "generatedSamples", 0, "video")
	if video == nil {
		video = getValue(out, "response", "generatedVideos", 0, "video")
	}
	return c.asset(text(video, "uri"), text(video, "bytesBase64Encoded"))
}
