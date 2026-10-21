package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vista/internal/store"
)

func TestResourceStorageHTTPScansAndCleansInBackground(t *testing.T) {
	app, session := inputQueueFixture(t)
	image := store.Image{MediaType: "image/png", Name: "picture.png", Data: testPNG(t)}
	if _, _, err := app.store.QueueInputWithImages(app.ctx, session, "image", "later", "", []store.Image{image}); err != nil {
		t.Fatal(err)
	}
	call := func(method, body string) (*httptest.ResponseRecorder, resourceStorageStatus) {
		t.Helper()
		w := httptest.NewRecorder()
		app.handler().ServeHTTP(w, httptest.NewRequest(method, "http://127.0.0.1:5186/api/resource-storage", strings.NewReader(body)))
		var result resourceStorageStatus
		if w.Code < 300 && json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("invalid storage response: %s", w.Body.String())
		}
		return w, result
	}
	wait := func() resourceStorageStatus {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			_, status := call(http.MethodGet, "")
			if status.Phase == "idle" {
				if status.Error != "" {
					t.Fatal(status.Error)
				}
				return status
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("storage operation did not finish")
		return resourceStorageStatus{}
	}
	response, _ := call(http.MethodPost, `{"action":"scan"}`)
	if response.Code != http.StatusAccepted || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("scan was not asynchronous: %d %s", response.Code, response.Body.String())
	}
	status := wait()
	if status.Stats == nil || status.Stats.ReferencedCount != 1 || status.Stats.CleanableCount != 0 {
		t.Fatalf("scan lost session ownership: %+v", status)
	}
	if err := app.store.Delete(app.ctx, session); err != nil {
		t.Fatal(err)
	}
	call(http.MethodPost, `{"action":"clean"}`)
	status = wait()
	if status.DeletedCount != 1 || status.DeletedBytes != int64(len(image.Data)) || status.Stats.TotalCount != 0 {
		t.Fatalf("cleanup did not report reclaimed originals: %+v", status)
	}
	for _, body := range []string{`{}`, `{"action":"anything"}`, `{"action":"clean","other":true}`, `{"action":"scan"}{}`} {
		if response, _ := call(http.MethodPost, body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid action accepted: %s %d", body, response.Code)
		}
	}
}
