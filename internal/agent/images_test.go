package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vista/internal/instance"
	modelcfg "vista/internal/model"
	"vista/internal/resources"
	"vista/internal/workerproto"
)

func TestImageCommandReachesModelAsImagePart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	image, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		last := request.Messages[len(request.Messages)-1]
		var parts []map[string]any
		if err := json.Unmarshal(last.Content, &parts); err != nil {
			t.Error(err)
		}
		if last.Role != "user" || len(parts) != 2 || !strings.HasPrefix(parts[0]["text"].(string), "describe") {
			t.Errorf("model did not receive user text and image: %+v", last)
		} else {
			url := parts[1]["image_url"].(map[string]any)["url"].(string)
			if !strings.HasPrefix(url, "data:image/png;base64,") {
				t.Errorf("image wire type: %q", url)
			} else if decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,")); err != nil || !bytes.Equal(decoded, image) {
				t.Errorf("image bytes changed: %v", err)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"A small image.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "image-run", Prompt: "describe", Images: []workerproto.Image{putResourceFixture(t, image, "image/png")}, Model: modelcfg.Model{Provider: "openai-chat", ID: "test", APIKey: "key", BaseURL: server.URL}}
	answer, err := Run(context.Background(), command, func(workerproto.Result) error { return nil })
	if err != nil || answer != "A small image." {
		t.Fatalf("image Agent run: answer=%q err=%v", answer, err)
	}
}

func putResourceFixture(t *testing.T, data []byte, mediaType string) resources.Ref {
	t.Helper()
	dir, err := instance.Dir()
	if err != nil {
		t.Fatal(err)
	}
	library := resources.Library{StateDir: dir}
	unlock, err := library.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	hash, size, err := library.Put(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return resources.Ref{Hash: hash, MediaType: mediaType, Bytes: size}
}
