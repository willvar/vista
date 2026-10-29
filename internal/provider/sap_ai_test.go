package provider

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
	"time"

	modelcfg "vista/internal/model"
)

func TestSAPAIImageTemplateShapeAndLimit(t *testing.T) {
	image := testImage(t)
	image.Data = append(append([]byte(nil), image.Data...), bytes.Repeat([]byte{0}, (1<<20)-len(image.Data))...)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(image.Data)
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v2/inference/deployments/deployment-one/v2/completion" || r.Header.Get("Authorization") != "Bearer short-token" || r.Header.Get("AI-Resource-Group") != "default" || r.ContentLength <= int64(len(image.Data)) {
			t.Errorf("SAP image route, auth, or length: %s, %d", r.URL, r.ContentLength)
		}
		var body struct {
			Config struct {
				Modules struct {
					PromptTemplating struct {
						Prompt struct {
							Template []struct {
								Role    string           `json:"role"`
								Content []map[string]any `json:"content"`
							} `json:"template"`
						} `json:"prompt"`
					} `json:"prompt_templating"`
				} `json:"modules"`
			} `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		} else if len(body.Config.Modules.PromptTemplating.Prompt.Template) != 1 {
			t.Errorf("SAP image template: %+v", body)
		} else {
			message := body.Config.Modules.PromptTemplating.Prompt.Template[0]
			if message.Role != "user" || len(message.Content) != 3 || message.Content[0]["text"] != "describe {​{image}}" {
				t.Errorf("SAP image text part: %+v", message)
			} else {
				for _, part := range message.Content[1:] {
					imageURL, ok := part["image_url"].(map[string]any)
					if part["type"] != "image_url" || !ok || imageURL["url"] != dataURL {
						t.Error("SAP image URL or bytes changed")
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"final_result\":{\"choices\":[{\"delta\":{\"content\":\"seen\"},\"finish_reason\":\"stop_sequence\"}]}}\n\n")
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "sap-ai-core", ProviderID: "sap-ai-core", ID: "gpt-4.1", BaseURL: endpoint.URL + "/v2", APIKey: "short-token", DeploymentID: "deployment-one", ResourceGroup: "default"}
	message := ChatMessage{Role: "user", Content: "describe {{image}}", Images: []Image{image, image}}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil)
	if err != nil || turn.Text != "seen" || requests != 1 {
		t.Fatalf("SAP image turn: %+v, requests=%d, err=%v", turn, requests, err)
	}
	message.Images[0].Data = make([]byte, maxImageBytes+1)
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil); err == nil || !strings.Contains(err.Error(), "50 MiB") || requests != 1 {
		t.Fatalf("SAP accepted oversized image request: %v, requests=%d", err, requests)
	}
}

func TestSAPAIStreamIncompleteAndCancellation(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"final_result\":{\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}}\n\n")
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "sap-ai-core", ProviderID: "sap-ai-core", ID: "gpt-4.1", BaseURL: endpoint.URL + "/v2", APIKey: "short-token", DeploymentID: "deployment-one", ResourceGroup: "default"}
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err == nil || !strings.Contains(err.Error(), "before [DONE]") {
		t.Fatalf("incomplete SAP stream: %v", err)
	}
	blocked := make(chan struct{})
	stalling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"final_result\":{\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}}\n\n")
		w.(http.Flusher).Flush()
		close(blocked)
		<-r.Context().Done()
	}))
	defer stalling.Close()
	model.BaseURL = stalling.URL + "/v2"
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
		finished <- err
	}()
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("SAP stream did not reach the Server")
	}
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("canceled SAP stream succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled SAP stream did not stop")
	}
}
