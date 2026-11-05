package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestStatisticsResetRequiresConfirmationAndPublishesAfterCommit(t *testing.T) {
	c, _ := newResultConsumerTest(t)
	a := &App{store: c.store}
	ctx := context.Background()
	now := time.Now()
	tokens := int64(125)
	if _, err := c.store.RecordTokenUsage(ctx, "before-reset", &tokens, now); err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.RecordActivity(ctx, []int64{now.Unix() / 60}, now); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", `{}`, `{"confirm":"reset-all"}`, `{"confirm":"reset-statistics","extra":true}`, `{"confirm":"reset-statistics"} {}`, strings.Repeat(" ", 1024) + `{"confirm":"reset-statistics"}`} {
		w := httptest.NewRecorder()
		a.resetStatistics(w, httptest.NewRequest(http.MethodPost, "/api/statistics/reset", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || a.updates.snapshot().Revisions["statistics"] != 0 {
			t.Fatal("invalid reset changed statistics", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	a.resetStatistics(w, httptest.NewRequest(http.MethodGet, "/api/statistics/reset", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal("accepted reset without POST", w.Code)
	}
	stats, err := c.store.Statistics(ctx, 7, "UTC", time.Now())
	if err != nil || stats.Tokens != tokens || stats.ActiveMinutes != 1 {
		t.Fatal("rejected reset changed data", stats, err)
	}
	w = httptest.NewRecorder()
	a.resetStatistics(w, httptest.NewRequest(http.MethodPost, "/api/statistics/reset", strings.NewReader(`{"confirm":"reset-statistics"}`)))
	if w.Code != http.StatusNoContent || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	for topic, revision := range a.updates.snapshot().Revisions {
		if topic == "statistics" && revision != 1 || topic != "statistics" && revision != 0 {
			t.Fatal("unexpected reset notification", topic, revision)
		}
	}
	stats, err = c.store.Statistics(ctx, 7, "UTC", time.Now())
	if err != nil || stats.Tokens != 0 || stats.Requests != 0 || stats.ActiveMinutes != 0 {
		t.Fatal("notification preceded reset commit", stats, err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.resetStatistics(w, httptest.NewRequest(http.MethodPost, "/api/statistics/reset", strings.NewReader(`{"confirm":"reset-statistics"}`)))
	if w.Code != http.StatusInternalServerError || a.updates.snapshot().Revisions["statistics"] != 1 {
		t.Fatal("failed reset published an update", w.Code)
	}
}

func TestActivityHTTPOnlyPublishesCommittedNewMinutes(t *testing.T) {
	c, _ := newResultConsumerTest(t)
	a := &App{store: c.store}
	minute := time.Now().Unix() / 60
	for index, body := range []string{fmt.Sprintf(`{"minutes":[%d,%d]}`, minute, minute), fmt.Sprintf(`{"minutes":[%d]}`, minute), `{"minutes":[]}`, `{"minutes":[0]}`, fmt.Sprintf(`{"minutes":[%d],"keystrokes":"secret"}`, minute), `{"minutes":[1]} {}`} {
		w := httptest.NewRecorder()
		a.activity(w, httptest.NewRequest(http.MethodPost, "/api/activity", strings.NewReader(body)))
		if (w.Code == http.StatusNoContent) != (index < 2) {
			t.Fatal(index, w.Code, w.Body.String())
		}
		if a.updates.snapshot().Revisions["statistics"] != 1 {
			t.Fatal("duplicates or invalid reports published revisions")
		}
	}
	w := httptest.NewRecorder()
	a.statistics(w, httptest.NewRequest(http.MethodGet, "/api/statistics?days=7&timezone=Asia%2FHong_Kong", nil))
	var result struct {
		store.Statistics
		Version string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.Version != a.stateVersion("statistics") || result.ActiveMinutes != 1 || len(result.Days) != 7 || result.RetentionDays != 90 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	for _, query := range []string{"days=1000000", "days=-7", "days=x", "timezone=Not%2FAZone"} {
		w = httptest.NewRecorder()
		a.statistics(w, httptest.NewRequest(http.MethodGet, "/api/statistics?"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatal(query, w.Code)
		}
	}
}

func TestWorkerTokenUsagePersistsBeforeNotificationAndSurvivesRunCompletion(t *testing.T) {
	c, _ := newResultConsumerTest(t)
	total := int64(125)
	id := uuid.NewString()
	notified := 0
	c.changed = func(kind string) {
		if kind != "token_usage" {
			return
		}
		notified++
		stats, err := c.store.Statistics(c.ctx, 7, "UTC", time.Now())
		if err != nil || stats.Tokens != total {
			t.Fatal("notified before commit", stats, err)
		}
	}
	if _, err := c.store.FinishRun(c.ctx, c.sessionID, "run", "reply", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := consumeTestResult(t, c, workerproto.Result{Kind: "token_usage", TokenUsage: &workerproto.TokenUsage{ID: id, Tokens: &total}}); err != nil {
			t.Fatal(err)
		}
	}
	if notified != 1 {
		t.Fatal("duplicate usage published", notified)
	}
	negative := int64(-1)
	for _, usage := range []*workerproto.TokenUsage{nil, {ID: "bad-id", Tokens: &total}, {ID: uuid.NewString(), Tokens: &negative}} {
		if err := consumeTestResult(t, c, workerproto.Result{Kind: "token_usage", TokenUsage: usage}); err == nil {
			t.Fatal("accepted invalid usage", usage)
		}
	}
	point := (&App{}).updates.snapshot()
	encoded, _ := json.Marshal(point)
	if !strings.Contains(string(encoded), `"statistics":0`) || strings.Contains(string(encoded), `"days"`) {
		t.Fatal("statistics were preloaded into global stream", string(encoded))
	}
}
