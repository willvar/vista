package generation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

func (c request) responseImage(body wire) (Asset, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return Asset{}, err
	}
	endpoint := c.route("responses")
	if strings.HasSuffix(c.base.Path, "/responses") {
		endpoint = c.base.String()
	}
	resp, err := c.send(http.MethodPost, endpoint, bytes.NewReader(data), http.Header{"Accept": {"text/event-stream"}, "OpenAI-Beta": {"responses=experimental"}}, true)
	if err != nil {
		return Asset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Asset{}, errors.New("hosted image request failed")
	}
	scan := bufio.NewScanner(io.LimitReader(resp.Body, maxResponseJSON))
	scan.Buffer(make([]byte, 64<<10), maxResponseJSON)
	var image string
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event wire
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
			continue
		}
		switch text(event, "type") {
		case "response.output_item.done":
			item := event["item"]
			if text(item, "type") == "image_generation_call" && completedImage(item) != "" {
				image = completedImage(item)
			}
		case "response.completed":
			items, _ := getValue(event, "response", "output").([]any)
			for _, item := range items {
				if text(item, "type") == "image_generation_call" {
					// The final snapshot takes precedence over intermediate events.
					return c.asset("", completedImage(item))
				}
			}
			return c.asset("", image)
		case "response.failed", "response.incomplete", "error":
			return Asset{}, errors.New("hosted image generation failed")
		}
	}
	return Asset{}, errors.New("hosted image response did not complete")
}

func completedImage(item any) string {
	if state := text(item, "status"); state == "" || state == "completed" {
		return text(item, "result")
	}
	return ""
}

func (c request) apiURL(raw string) (string, error) {
	u, e := c.base.Parse(raw)
	if raw == "" || e != nil || !sameOrigin(c.base, u) || u.User != nil || u.Fragment != "" {
		return "", errors.New("invalid generation task URL")
	}
	return u.String(), nil
}

func (c request) fal(input Input, video bool) (Asset, error) {
	body := wire{"prompt": input.Prompt}
	if !video {
		body["num_images"] = 1
	}
	if frame := input.dataURL(); frame != "" {
		if strings.Contains(c.model, "seedance-2.0") || strings.Contains(c.model, "kling-video") {
			body["start_image_url"] = frame
		} else {
			body["image_url"] = frame
		}
	}
	out, err := c.api(http.MethodPost, c.route(c.model), body, nil)
	if err != nil {
		return Asset{}, err
	}
	statusURL, err := c.apiURL(text(out, "status_url"))
	if err != nil {
		return Asset{}, err
	}
	resultURL, err := c.apiURL(text(out, "response_url"))
	if err != nil {
		return Asset{}, err
	}
	_, err = c.poll(statusURL, func(o wire) (bool, error) { return status(text(o, "status")) })
	if err != nil {
		return Asset{}, err
	}
	result, err := c.api(http.MethodGet, resultURL, nil, nil)
	if err != nil {
		return Asset{}, err
	}
	if payload, ok := result["response"].(map[string]any); ok {
		result = wire(payload)
	}
	if video {
		return c.asset(first(text(result, "video", "url"), text(result, "videos", 0, "url")), "")
	}
	return c.asset(first(text(result, "images", 0, "url"), text(result, "image", "url")), "")
}

func (c request) vydra(input Input, video bool) (Asset, error) {
	body := wire{"prompt": input.Prompt}
	if !video {
		body["model"] = "text-to-image"
	}
	out, err := c.api(http.MethodPost, c.route("models/"+c.model), body, nil)
	if err != nil {
		return Asset{}, err
	}
	id := jobID(out, "jobId")
	if !validTaskID(id) {
		return Asset{}, errors.New("Vydra did not return a job ID")
	}
	done, err := status(text(out, "status"))
	if err != nil {
		return Asset{}, err
	}
	if !done {
		out, err = c.poll(c.route("jobs/"+id), func(o wire) (bool, error) { return status(text(o, "status")) })
		if err != nil {
			return Asset{}, err
		}
	}
	return c.asset(first(text(out, "imageUrl"), text(out, "videoUrl")), "")
}

func (c request) novita(input Input) (Asset, error) {
	body := wire{"prompt": input.Prompt, "duration": 6, "resolution": "768P"}
	if strings.HasPrefix(c.model, "wan") {
		params := wire{"duration": 5, "audio": false, "prompt_extend": true, "shot_type": "multi", "watermark": false}
		body = wire{"input": wire{"prompt": input.Prompt}, "parameters": params}
		if frame := input.dataURL(); frame != "" {
			body["input"].(wire)["img_url"] = frame
			params["resolution"] = "720P"
		} else {
			params["size"] = "1280*720"
		}
	} else if frame := input.dataURL(); frame != "" {
		body["image"] = frame
	}
	out, err := c.api(http.MethodPost, c.route("v3/async/"+c.model), body, nil)
	if err != nil {
		return Asset{}, err
	}
	id := jobID(out, "task_id")
	if !validTaskID(id) {
		return Asset{}, errors.New("Novita did not return a task ID")
	}
	out, err = c.poll(c.route("v3/async/task-result?task_id="+url.QueryEscape(id)), func(o wire) (bool, error) { return status(text(o, "task", "status")) })
	if err != nil {
		return Asset{}, err
	}
	return c.asset(first(text(out, "videos", 0, "video_url"), text(out, "output", 0, "video_url")), "")
}

func (c request) uploadFrame(path, field string, input Input) (wire, error) {
	var b bytes.Buffer
	mp := multipart.NewWriter(&b)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name": field, "filename": "frame." + strings.TrimPrefix(input.FrameType, "image/"),
	}))
	header.Set("Content-Type", input.FrameType)
	part, err := mp.CreatePart(header)
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(input.Frame); err != nil {
		return nil, err
	}
	if err = mp.Close(); err != nil {
		return nil, err
	}
	resp, err := c.send(http.MethodPost, c.route(path), &b, http.Header{"Content-Type": {mp.FormDataContentType()}}, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("reference image upload failed")
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	decoder.UseNumber()
	var out wire
	if decoder.Decode(&out) != nil || out == nil {
		return nil, errors.New("invalid reference image upload response")
	}
	return out, c.resultError(out)
}

func (c request) pixverse(input Input) (Asset, error) {
	path := "video/text/generate"
	body := wire{"model": strings.TrimPrefix(c.model, "pixverse/"), "prompt": input.Prompt, "duration": 5, "quality": "540p", "aspect_ratio": "16:9"}
	if len(input.Frame) > 0 {
		o, err := c.uploadFrame("image/upload", "image", input)
		if err != nil {
			return Asset{}, err
		}
		id, ok := numeric(getValue(o, "Resp", "img_id"))
		if !ok || id <= 0 {
			return Asset{}, errors.New("PixVerse did not return a valid image ID")
		}
		path = "video/img/generate"
		delete(body, "aspect_ratio")
		body["img_id"] = id
		body["motion_mode"] = "normal"
	}
	out, err := c.api(http.MethodPost, c.route(path), body, nil)
	if err != nil {
		return Asset{}, err
	}
	id := jobID(out, "Resp", "video_id")
	if !validTaskID(id) {
		return Asset{}, errors.New("PixVerse did not return a video ID")
	}
	out, err = c.poll(c.route("video/result/"+id), func(o wire) (bool, error) {
		s, ok := numeric(getValue(o, "Resp", "status"))
		if !ok {
			return false, errors.New("invalid PixVerse video status")
		}
		switch s {
		case 1:
			return true, nil
		case 5:
			return false, nil
		case 6:
			return true, ErrUpstreamCanceled
		default:
			return false, errors.New("PixVerse video generation failed")
		}
	})
	if err != nil {
		return Asset{}, err
	}
	return c.asset(text(out, "Resp", "url"), "")
}

func (c request) kie(input Input) (Asset, error) {
	params := wire{"prompt": input.Prompt, "aspect_ratio": "16:9", "duration": "5", "sound": false}
	out, err := c.api(http.MethodPost, c.route("api/v1/jobs/createTask"), wire{"model": c.model, "input": params}, nil)
	if err != nil {
		return Asset{}, err
	}
	id := jobID(out, "data", "taskId")
	if id == "" || len(id) > 256 {
		return Asset{}, errors.New("Kie did not return a task ID")
	}
	out, err = c.poll(c.route("api/v1/jobs/recordInfo?taskId="+url.QueryEscape(id)), func(o wire) (bool, error) {
		s := text(o, "data", "state")
		if s == "waiting" || s == "queuing" || s == "generating" {
			return false, nil
		}
		return status(s)
	})
	if err != nil {
		return Asset{}, err
	}
	var result wire
	if err = json.Unmarshal([]byte(text(out, "data", "resultJson")), &result); err != nil {
		return Asset{}, errors.New("invalid Kie output list")
	}
	return c.asset(text(result, "resultUrls", 0), "")
}

func (c request) comfy(input Input, video bool) (Asset, error) {
	var opt struct {
		Workflow    map[string]wire `json:"workflow"`
		PromptNode  string          `json:"promptNode"`
		PromptField string          `json:"promptField"`
		ImageNode   string          `json:"imageNode"`
		ImageField  string          `json:"imageField"`
		OutputNode  string          `json:"outputNode"`
		Cloud       bool            `json:"cloud"`
	}
	if json.Unmarshal(c.target.Options, &opt) != nil || opt.Workflow == nil || opt.PromptNode == "" || opt.OutputNode == "" {
		return Asset{}, errors.New("ComfyUI requires a saved workflow, prompt node and output node")
	}
	if opt.PromptField == "" {
		opt.PromptField = "text"
	}
	if opt.ImageField == "" {
		opt.ImageField = "image"
	}
	set := func(node, field, value string) bool {
		inputs, ok := opt.Workflow[node]["inputs"].(map[string]any)
		if ok {
			inputs[field] = value
		}
		return ok
	}
	if !set(opt.PromptNode, opt.PromptField, input.Prompt) {
		return Asset{}, errors.New("ComfyUI prompt node is missing")
	}
	prefix := ""
	if opt.Cloud && !strings.HasSuffix(strings.TrimRight(c.base.Path, "/"), "/api") {
		prefix = "api/"
	}
	if len(input.Frame) > 0 {
		if opt.ImageNode == "" || !set(opt.ImageNode, opt.ImageField, "") {
			return Asset{}, errors.New("this ComfyUI workflow has no reference image input")
		}
		out, err := c.uploadFrame(prefix+"upload/image", "image", input)
		if err != nil {
			return Asset{}, err
		}
		name := text(out, "name")
		if name == "" || !set(opt.ImageNode, opt.ImageField, name) {
			return Asset{}, errors.New("ComfyUI image input is missing")
		}
	}
	out, err := c.api(http.MethodPost, c.route(prefix+"prompt"), wire{"prompt": opt.Workflow}, nil)
	if err != nil {
		return Asset{}, err
	}
	id := jobID(out, "prompt_id")
	if !validTaskID(id) {
		return Asset{}, errors.New("ComfyUI did not return a prompt ID")
	}
	var outputs any
	if opt.Cloud {
		out, err = c.poll(c.route(prefix+"jobs/"+id), func(o wire) (bool, error) { return status(text(o, "status")) })
		outputs = out["outputs"]
	} else {
		out, err = c.poll(c.route("history/"+id), func(o wire) (bool, error) {
			if text(o, id, "status", "status_str") == "error" {
				return false, errors.New("ComfyUI workflow failed")
			}
			return getValue(o, id, "outputs") != nil, nil
		})
		outputs = getValue(out, id, "outputs")
	}
	if err != nil {
		return Asset{}, err
	}
	kinds := []string{"images"}
	if video {
		kinds = []string{"videos", "gifs", "images"}
	}
	for _, kind := range kinds {
		a := getValue(outputs, opt.OutputNode, kind, 0)
		if name := text(a, "filename"); name != "" {
			q := url.Values{"filename": {name}, "subfolder": {text(a, "subfolder")}, "type": {first(text(a, "type"), "output")}}
			return c.asset(c.route(prefix+"view?"+q.Encode()), "")
		}
	}
	return Asset{}, errors.New("ComfyUI output node did not return media")
}
