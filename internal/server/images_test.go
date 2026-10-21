package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/modelcatalog"
	"vista/internal/store"
	"vista/internal/workerproto"
)

type imageCommandSink struct{ bytes.Buffer }

func (*imageCommandSink) Close() error { return nil }

func testPNG(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImageSubmissionDispatchAndReadWithoutToken(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000093"
	if _, err := s.Create(ctx, session, "image run"); err != nil {
		t.Fatal(err)
	}
	sink := &imageCommandSink{}
	models := modelCatalog{Default: "image", Models: []configuredModel{{Key: "image", Provider: "openai-chat", ID: "test", BaseURL: "http://127.0.0.1:1/v1", APIKey: "key", InputModalities: []string{"text", "image"}}}}
	if err := modelcatalog.Validate(models); err != nil {
		t.Fatal(err)
	}
	app := &App{store: s, models: models, workers: map[string]*worker{session: {input: sink}}}
	data := testPNG(t)
	data = append(data, make([]byte, store.MaxImageBytes-len(data))...)
	image := map[string]any{"mediaType": "image/png", "data": base64.StdEncoding.EncodeToString(data)}
	body, _ := json.Marshal(map[string]any{"content": "", "modelKey": "image", "images": []any{image, image, image, image}})
	if len(body) <= 4<<20 {
		t.Fatal("fixture does not exceed the old HTTP and IPC size limits")
	}
	w := httptest.NewRecorder()
	app.submitMessage(w, httptest.NewRequest(http.MethodPost, "/api/sessions/"+session+"/messages", bytes.NewReader(body)), session)
	if w.Code != http.StatusAccepted {
		t.Fatalf("image submission status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		Message store.Message `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Message.ImageCount != 4 {
		t.Fatalf("image response: %+v %v", result, err)
	}
	var command workerproto.Command
	if err := json.NewDecoder(&sink.Buffer).Decode(&command); err != nil || len(command.Images) != 4 || command.Prompt != "" || command.Images[3].Hash != fmt.Sprintf("%x", sha256.Sum256(data)) {
		t.Fatalf("worker image command: images=%d prompt=%q err=%v", len(command.Images), command.Prompt, err)
	}
	imagePath := "/api/sessions/" + session + "/messages/" + result.Message.ID + "/images/3"
	got := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5186"+imagePath, nil)
	app.handler().ServeHTTP(got, request)
	if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "image/png" || !bytes.Equal(got.Body.Bytes(), data) {
		t.Fatalf("image read status=%d type=%s", got.Code, got.Header().Get("Content-Type"))
	}
	if got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("image cache/content headers: %v", got.Header())
	}
	missing := httptest.NewRecorder()
	app.session(missing, httptest.NewRequest(http.MethodGet, "/api/sessions/"+session+"/messages/"+result.Message.ID+"/images/4", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing image status=%d", missing.Code)
	}
	const anotherSession = "00000000-0000-4000-8000-000000000095"
	if _, err := s.Create(ctx, anotherSession, "other Session"); err != nil {
		t.Fatal(err)
	}
	foreign := httptest.NewRecorder()
	app.session(foreign, httptest.NewRequest(http.MethodGet, "/api/sessions/"+anotherSession+"/messages/"+result.Message.ID+"/images/0", nil))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-Session image read status=%d", foreign.Code)
	}
	if _, err := s.FinishRun(ctx, session, command.RunID, "answer", "succeeded", "seen"); err != nil {
		t.Fatal(err)
	}
	sink.Reset()
	next := httptest.NewRecorder()
	app.submitMessage(next, httptest.NewRequest(http.MethodPost, "/messages", bytes.NewReader(body)), session)
	if next.Code != http.StatusAccepted {
		t.Fatalf("follow-up image submission: status=%d body=%s", next.Code, next.Body.String())
	}
	var followup workerproto.Command
	if err := json.NewDecoder(&sink.Buffer).Decode(&followup); err != nil {
		t.Fatal(err)
	}
	if len(followup.Images) != 4 || followup.MessageSequence <= command.MessageSequence || len(followup.History) != 0 {
		t.Fatalf("follow-up must reference durable context: sequence=%d history=%d", followup.MessageSequence, len(followup.History))
	}
	retained, err := s.MessageImages(ctx, session, result.Message.ID)
	if err != nil || len(retained) != 4 || !bytes.Equal(retained[3].Data, data) {
		t.Fatalf("durable images lost: %v", err)
	}

}

func TestImageSubmissionRejectsUnsupportedModelAndInvalidPayload(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const session = "00000000-0000-4000-8000-000000000094"
	if _, err := s.Create(context.Background(), session, "image rejection"); err != nil {
		t.Fatal(err)
	}
	models := modelCatalog{Default: "text", Models: []configuredModel{{Key: "text", Provider: "openai-chat", ID: "test", APIKey: "key"}}}
	app := &App{store: s, models: models, workers: map[string]*worker{session: {input: &imageCommandSink{}}}}
	image := base64.StdEncoding.EncodeToString(testPNG(t))
	for _, tc := range []struct {
		name, mediaType, data, want string
		enabled                     bool
	}{
		{"unsupported model", "image/png", image, "does not support image", false},
		{"wrong media type", "image/jpeg", image, "does not match content", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.enabled {
				app.models.Models[0].InputModalities = []string{"text", "image"}
			}
			body, _ := json.Marshal(map[string]any{"content": "look", "images": []any{map[string]any{"mediaType": tc.mediaType, "data": tc.data}}})
			w := httptest.NewRecorder()
			app.submitMessage(w, httptest.NewRequest(http.MethodPost, "/messages", bytes.NewReader(body)), session)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("rejection status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	count, err := s.SessionCounts(context.Background())
	if err != nil || count.Running != 0 {
		t.Fatalf("rejected input started a run: %+v %v", count, err)
	}
}
