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

func TestSessionTitleSortPrecedesLimitAndSharesOpenWindow(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	ctx := consumer.ctx
	create := func(id, title string) {
		t.Helper()
		if _, err := app.store.Create(ctx, id, title); err != nil {
			t.Fatal(err)
		}
	}
	create("000-older", "北京")
	for i := range 1001 {
		id := fmt.Sprintf("filler-%04d", i)
		create(id, "Zulu "+id)
	}
	create("apple-1", "Apple")
	create("apple-2", "APPLE")
	create("chrome", "Chrome")
	create("beta", "Beta")
	create("pin", "Z pinned")
	create("closed", "Aaron")
	create("untitled", "New session")
	if err := app.store.SetPinned(ctx, "pin", true); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetOpen(ctx, "closed", false); err != nil {
		t.Fatal(err)
	}
	open, err := app.store.List(ctx, true)
	if err != nil || len(open) != 1000 {
		t.Fatalf("legacy open list: %d %v", len(open), err)
	}
	for _, row := range open {
		if row.ID == "000-older" {
			t.Fatal("fixture did not place the old title beyond the list limit")
		}
	}
	w := httptest.NewRecorder()
	app.sessions(w, httptest.NewRequest(http.MethodGet, "/api/sessions?page=1&pageSize=7&open=true&sort=title-asc&untitledLabel=Aardvark", nil))
	var result struct {
		store.SessionPage
		Version string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatalf("title order: %d %s %v", w.Code, w.Body.String(), err)
	}
	want := []string{"pin", "untitled", "apple-1", "apple-2", "000-older", "beta", "chrome"}
	if len(result.Items) != len(want) || result.Total <= 1000 || result.Attention == nil || *result.Attention != result.Total {
		t.Fatalf("wrong page or aggregate: %s", w.Body.String())
	}
	for i, row := range result.Items {
		if row.ID != want[i] {
			t.Fatalf("row %d = %s, want %s", i, row.ID, want[i])
		}
	}
	previous := app.updates.snapshot()
	app.updates.notify("sessions")
	point, err := app.stateUpdateScoped(ctx, "localhost", previous, map[string]bool{}, &sessionWindowInterest{Size: 7, Sort: store.SessionTitleAZ, UntitledLabel: "Aardvark"})
	if err != nil || point.Sessions != nil || point.SessionWindow == nil || point.SessionWindow.Version != app.stateVersion("sessions") {
		t.Fatalf("scoped push: %+v %v", point, err)
	}
	for i, row := range point.SessionWindow.Items {
		if row.ID != want[i] {
			t.Fatalf("pushed row %d = %s, want %s", i, row.ID, want[i])
		}
	}
}
