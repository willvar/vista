package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestManagementPaginationRejectsInvalidParametersBeforeWriting(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	for _, query := range []string{"?page=0", "?page=-1", "?page=x", "?page=", "?page=1&page=2", "?pageSize=0", "?pageSize=101", "?pageSize=1&pageSize=2", "?page=99999999999999999999999999999"} {
		for _, path := range []string{"/api/sessions", "/api/memories"} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, path+query, nil)
			if path == "/api/sessions" {
				app.sessions(w, r)
			} else {
				app.memories(w, r)
			}
			if w.Code != http.StatusBadRequest {
				t.Fatalf("accepted %s%s: %d %s", path, query, w.Code, w.Body.String())
			}
		}
		w := httptest.NewRecorder()
		app.memories(w, httptest.NewRequest(http.MethodPost, "/api/memories"+query, strings.NewReader(`{"action":"add","text":"Must not save"}`)))
		items, err := app.store.Memories(consumer.ctx)
		if w.Code != http.StatusBadRequest || err != nil || len(items) != 0 || app.updates.snapshot().Revisions["memories"] != 0 {
			t.Fatalf("invalid pagination wrote memory: %d %+v %v", w.Code, items, err)
		}
	}
}

func TestSessionManagementReturnsOnlyRequestedPageAndFullSearchCount(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	for i := range 25 {
		id := fmt.Sprintf("archived-%02d", i)
		if _, err := app.store.Create(consumer.ctx, id, fmt.Sprintf("Archived %02d", i)); err != nil {
			t.Fatal(err)
		}
		if err := app.store.SetOpen(consumer.ctx, id, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query                string
		page, size, total, n int
	}{
		{"?page=1", 1, 10, 26, 10},
		{"?page=2&pageSize=20&open=false", 2, 20, 25, 5},
		{"?page=999&pageSize=10&open=false", 3, 10, 25, 5},
		{"?pageSize=50&open=false", 1, 50, 25, 25},
		{"?page=999&search=archived%2000", 1, 10, 1, 1},
		{"?page=2&open=true", 1, 10, 1, 1},
	} {
		w := httptest.NewRecorder()
		app.sessions(w, httptest.NewRequest(http.MethodGet, "/api/sessions"+tc.query, nil))
		var result struct {
			store.SessionPage
			Version string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Page != tc.page || result.PageSize != tc.size || result.Total != tc.total || len(result.Items) != tc.n || result.Version != app.stateVersion("sessions") {
			t.Fatalf("%s: status=%d %s error=%v", tc.query, w.Code, w.Body.String(), err)
		}
		if strings.Contains(w.Body.String(), `"messages"`) || strings.Contains(w.Body.String(), `"events"`) {
			t.Fatal("management page included conversation history")
		}
	}
}

func TestSessionManagementValidatesAndAppliesSorting(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	for _, query := range []string{
		"?sort=updated-desc", "?page=1&sort=invalid", "?page=1&sort=updated-desc&sort=created-desc",
		"?page=1&sort=created_at%20DESC", "?page=1&sort=UPDATED-DESC",
	} {
		w := httptest.NewRecorder()
		app.sessions(w, httptest.NewRequest(http.MethodGet, "/api/sessions"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("accepted sort %s: %d %s", query, w.Code, w.Body.String())
		}
	}
	for _, id := range []string{"alpha", "omega"} {
		if _, err := app.store.Create(consumer.ctx, id, "Sorting API "+id); err != nil {
			t.Fatal(err)
		}
		if err := app.store.SetOpen(consumer.ctx, id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.store.Rename(consumer.ctx, "alpha", "Sorting API updated"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sort string
		id   string
	}{
		{"created-desc", "omega"}, {"created-asc", "alpha"},
		{"updated-desc", "alpha"}, {"updated-asc", "omega"},
	} {
		w := httptest.NewRecorder()
		app.sessions(w, httptest.NewRequest(http.MethodGet, "/api/sessions?page=1&pageSize=1&open=false&search=Sorting%20API&sort="+tc.sort, nil))
		var result store.SessionPage
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Total != 2 || len(result.Items) != 1 || result.Items[0].ID != tc.id {
			t.Fatalf("sort %s: %d %s %v", tc.sort, w.Code, w.Body.String(), err)
		}
	}
}

func TestMemoryManagementPagesAndDeleteClampAreScopedToActivePage(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	for i := range 21 {
		if err := app.store.ChangeMemory(consumer.ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: fmt.Sprintf("Preference %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(path string, method string, body string) memoryPageSnapshot {
		t.Helper()
		w := httptest.NewRecorder()
		app.memories(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		var result memoryPageSnapshot
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Version != app.stateVersion("memories") || result.MaxCount != store.MaxMemories || result.MaxBytes != store.MaxMemoryBytes || result.MaxTotalBytes != store.MaxMemoriesBytes {
			t.Fatalf("%s %s: %d %s %v", method, path, w.Code, w.Body.String(), err)
		}
		return result
	}
	first := read("/api/memories?page=1&pageSize=10", http.MethodGet, "")
	last := read("/api/memories?page=3&pageSize=10", http.MethodGet, "")
	if len(first.Memories) != 10 || first.Total != 21 || len(last.Memories) != 1 || last.Memories[0].Text != "Preference 20" {
		t.Fatalf("wrong pages: %+v %+v", first, last)
	}
	remove := workerproto.MemoryChange{Action: "remove", ID: last.Memories[0].ID, PreviousText: last.Memories[0].Text}
	body, _ := json.Marshal(remove)
	clamped := read("/api/memories?page=3&pageSize=10", http.MethodPost, string(body))
	if clamped.Page != 2 || clamped.Total != 20 || len(clamped.Memories) != 10 || clamped.Memories[0].Text != "Preference 10" {
		t.Fatalf("last deletion left an unreachable page: %+v", clamped)
	}
	point, err := app.stateUpdate(consumer.ctx, "localhost", stateCheckpoint{})
	encoded, _ := json.Marshal(point)
	if err != nil || point.Memories == nil || point.Memories.Total != 20 || strings.Contains(string(encoded), "Preference") {
		t.Fatalf("memory stream must carry a lightweight summary: %s %v", encoded, err)
	}
}
