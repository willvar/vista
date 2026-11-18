package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vista/internal/store"
)

func TestManagementPaginationRejectsInvalidParameters(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	for _, query := range []string{"?page=0", "?page=-1", "?page=x", "?page=", "?page=1&page=2", "?pageSize=0", "?pageSize=101", "?pageSize=1&pageSize=2", "?page=99999999999999999999999999999"} {
		w := httptest.NewRecorder()
		app.sessions(w, httptest.NewRequest(http.MethodGet, "/api/sessions"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s: %d %s", query, w.Code, w.Body.String())
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
