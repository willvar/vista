package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"vista/internal/provider"
)

func TestOMPCatalogCheckPersistsScheduleAndSeparatesCheckFromUpdate(t *testing.T) {
	app := catalogUpdateTestApp(t)
	app.initCatalogUpdates()
	assertStatus := func(changed bool, failed bool) catalogUpdateStatus {
		t.Helper()
		status := app.catalogUpdateStatus()
		if status.CheckedAt.IsZero() || status.Updating || status.LastCheckChanged != changed || (status.CheckError != "") != failed {
			t.Fatalf("incorrect check outcome: %+v", status)
		}
		if len(status.History) == 0 || !status.History[0].CheckedAt.Equal(status.CheckedAt) || status.History[0].Changed != changed || status.History[0].Error != status.CheckError || status.History[0].Automatic {
			t.Fatalf("manual check missing from history: %+v", status.History)
		}
		if status.History[0].OMPCatalogInfo != provider.CurrentOMPCatalog().Info() {
			t.Fatal("history did not capture the installed catalog at check completion")
		}
		delay := status.NextCheckAt.Sub(status.CheckedAt)
		if failed && delay != catalogRetryInterval || !failed && (delay < catalogCheckInterval || delay >= catalogCheckInterval+catalogCheckJitter) {
			t.Fatalf("incorrect next check: %v", delay)
		}
		// A new App reads the same schedule and result without checking upstream.
		restarted := &App{configDir: app.configDir}
		restarted.initCatalogUpdates()
		if got := restarted.catalogUpdateStatus(); !reflect.DeepEqual(got, status) {
			t.Fatalf("restart lost check outcome: got %+v, want %+v", got, status)
		}
		return status
	}
	if changed, err := app.checkModelCatalog(context.Background(), false); err != nil || !changed {
		t.Fatalf("initial check: changed=%v err=%v", changed, err)
	}
	first := assertStatus(true, false)
	updatedAt := provider.CurrentOMPCatalog().Info().UpdatedAt
	if changed, err := app.checkModelCatalog(context.Background(), false); err != nil || changed {
		t.Fatalf("unchanged check: changed=%v err=%v", changed, err)
	}
	second := assertStatus(false, false)
	if len(second.History) != 2 || !second.History[1].Changed || second.History[1].CheckedAt != first.CheckedAt {
		t.Fatal("unchanged check replaced the earlier update record")
	}
	if !second.CheckedAt.After(first.CheckedAt) || provider.CurrentOMPCatalog().Info().UpdatedAt != updatedAt {
		t.Fatal("unchanged check failed to advance check time or changed the actual update time")
	}
	current := provider.CurrentOMPCatalog()
	before, err := os.ReadFile(provider.OMPCatalogPath(app.configDir))
	if err != nil {
		t.Fatal(err)
	}
	app.ompCatalogClient = &http.Client{Transport: catalogTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("offline"))}, nil
	})}
	if changed, err := app.checkModelCatalog(context.Background(), false); err == nil || changed {
		t.Fatalf("failed check: changed=%v err=%v", changed, err)
	}
	failed := assertStatus(false, true)
	if len(failed.History) != 3 || failed.History[0].Error == "" {
		t.Fatal("failed check did not retain its outcome alongside earlier records")
	}
	after, err := os.ReadFile(provider.OMPCatalogPath(app.configDir))
	if err != nil || !bytes.Equal(before, after) || provider.CurrentOMPCatalog() != current {
		t.Fatal("failed scheduled check replaced installed metadata")
	}
	// Manual checks are allowed before the retry deadline and clear old errors.
	app.ompCatalogClient = catalogTestClient(updatedCatalogSource)
	response := httptest.NewRecorder()
	app.ompCatalog(response, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("manual retry: %d %s", response.Code, response.Body)
	}
	if status := assertStatus(false, false); len(status.History) != 4 || status.History[1].Error == "" {
		t.Fatal("successful retry erased the earlier failure")
	}
}

func TestOMPCatalogFieldConflictIsReturnedAndPersistedWithoutReplacingData(t *testing.T) {
	app := catalogUpdateTestApp(t)
	app.initCatalogUpdates()
	if _, err := app.checkModelCatalog(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(provider.OMPCatalogPath(app.configDir))
	if err != nil {
		t.Fatal(err)
	}
	current := provider.CurrentOMPCatalog()
	candidate := strings.Replace(updatedModelsDevSource, `"context":200000`, `"context":50000`, 1)
	witness := strings.Replace(updatedModelsDevSource, `"context":200000`, `"context":64000`, 1)
	standard := catalogTestClientWithModelsDev(updatedCatalogSource, candidate)
	independent := catalogTestClientWithModelsDev(updatedCatalogSource, witness)
	app.ompCatalogClient = &http.Client{Transport: catalogTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "catalog.stencil.so" {
			return independent.Transport.RoundTrip(r)
		}
		return standard.Transport.RoundTrip(r)
	})}
	response := httptest.NewRecorder()
	app.ompCatalog(response, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "contextWindow") || !strings.Contains(response.Body.String(), "50000") || !strings.Contains(response.Body.String(), "64000") {
		t.Fatalf("missing model-field conflict in API error: %d %s", response.Code, response.Body)
	}
	status, err := loadCatalogUpdateStatus(app.configDir)
	if err != nil || !strings.Contains(status.CheckError, "contextWindow") || len(status.History) != 2 || status.History[0].Error != status.CheckError {
		t.Fatalf("conflict details did not survive reload: %+v %v", status, err)
	}
	after, err := os.ReadFile(provider.OMPCatalogPath(app.configDir))
	if err != nil || !bytes.Equal(before, after) || provider.CurrentOMPCatalog() != current {
		t.Fatal("conflict replaced the installed snapshot")
	}
}

func TestOMPCatalogHistoryExpiresAfterSevenDaysOnReadReloadAndSave(t *testing.T) {
	app := directoryTestApp(t)
	now := time.Now().UTC()
	expired := catalogCheckRecord{CheckedAt: now.Add(-catalogHistoryWindow - time.Second), OMPCatalogInfo: provider.OMPCatalogInfo{Revision: "expired"}}
	recent := catalogCheckRecord{CheckedAt: now.Add(-catalogHistoryWindow + time.Hour), OMPCatalogInfo: provider.OMPCatalogInfo{Revision: "recent"}, Automatic: true}
	data, err := json.Marshal(catalogUpdateStatus{History: []catalogCheckRecord{recent, expired}})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a restart after records on disk have crossed the retention window.
	if err := os.WriteFile(catalogUpdateStatusPath(app.configDir), data, 0600); err != nil {
		t.Fatal(err)
	}
	app.initCatalogUpdates()
	if history := app.catalogUpdateStatus().History; !reflect.DeepEqual(history, []catalogCheckRecord{recent}) {
		t.Fatalf("restart retained expired history: %+v", history)
	}
	// Reads prune by time too, even without another check, and return an isolated
	// slice so HTTP serialization cannot race with the next completed check.
	app.ompCatalogStatus.History = append(app.ompCatalogStatus.History, expired)
	status := app.catalogUpdateStatus()
	if len(status.History) != 1 {
		t.Fatal("read exposed expired history")
	}
	status.History[0].Error = "changed returned copy"
	if app.ompCatalogStatus.History[0].Error != "" {
		t.Fatal("status read exposed mutable stored records")
	}
	app.finishCatalogCheck(false, false, nil)
	data, err = os.ReadFile(catalogUpdateStatusPath(app.configDir))
	if err != nil || bytes.Contains(data, []byte("expired")) || bytes.Contains(data, []byte("changed returned copy")) {
		t.Fatalf("saved history retained expired or externally changed records: %s %v", data, err)
	}
	response := httptest.NewRecorder()
	app.ompCatalog(response, httptest.NewRequest(http.MethodGet, "/api/model-catalog", nil))
	var info modelCatalogInfo
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil || len(info.History) != 2 || info.History[1].Revision != "recent" || !info.History[1].Automatic {
		t.Fatalf("history API: %s %v", response.Body, err)
	}
}

func TestOMPCatalogPollRestoresDeadlineSerializesAndStopsOnShutdown(t *testing.T) {
	for _, start := range []string{"first startup", "overdue", "future"} {
		t.Run(start, func(t *testing.T) {
			app := catalogUpdateTestApp(t)
			previous := catalogUpdateStatus{}
			if start != "first startup" {
				previous = catalogUpdateStatus{CheckedAt: time.Now().Add(-time.Hour).UTC(), NextCheckAt: time.Now().Add(-time.Minute).UTC(), CheckError: "previous failure"}
				if start == "future" {
					previous.NextCheckAt = time.Now().Add(time.Hour).UTC()
				}
				if err := saveCatalogUpdateStatus(app.configDir, previous); err != nil {
					t.Fatal(err)
				}
			}
			app.initCatalogUpdates()
			entered := make(chan struct{})
			var once sync.Once
			app.ompCatalogClient = &http.Client{Transport: catalogTestTransport(func(r *http.Request) (*http.Response, error) {
				once.Do(func() { close(entered) })
				<-r.Context().Done()
				return nil, r.Context().Err()
			})}
			ctx, cancel := context.WithCancel(context.Background())
			app.ctx = ctx
			done := make(chan struct{})
			go func() { defer close(done); app.pollModelCatalog(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("catalog poll ignored server shutdown")
				}
			})
			if start == "future" {
				select {
				case <-entered:
					t.Fatal("restart checked upstream before its saved deadline")
				case <-time.After(50 * time.Millisecond):
				}
				app.ompCatalogStatusMu.Lock()
				app.ompCatalogStatus.NextCheckAt = time.Now().Add(-time.Minute)
				app.ompCatalogStatusMu.Unlock()
				app.ompCatalogWake <- struct{}{}
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("due automatic check did not start")
			}
			get := httptest.NewRecorder()
			app.ompCatalog(get, httptest.NewRequest(http.MethodGet, "/api/model-catalog", nil))
			if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"updating":true`) {
				t.Fatalf("automatic progress unavailable: %d %s", get.Code, get.Body)
			}
			post := httptest.NewRecorder()
			app.ompCatalog(post, httptest.NewRequest(http.MethodPost, "/api/model-catalog", nil))
			if post.Code != http.StatusConflict {
				t.Fatalf("manual check overlapped automatic download: %d", post.Code)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown failed to cancel download")
			}
			if status := app.catalogUpdateStatus(); status.Updating {
				t.Fatal("shutdown left transient updating state")
			}
			restored, err := loadCatalogUpdateStatus(app.configDir)
			if err != nil || !reflect.DeepEqual(restored, previous) {
				t.Fatalf("shutdown changed the saved retry deadline: %+v %v", restored, err)
			}
		})
	}
}

func TestOMPCatalogPollReschedulesAfterAutomaticSuccess(t *testing.T) {
	app := catalogUpdateTestApp(t)
	app.initCatalogUpdates()
	ctx, cancel := context.WithCancel(context.Background())
	app.ctx = ctx
	done := make(chan struct{})
	go func() { defer close(done); app.pollModelCatalog(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		status := app.catalogUpdateStatus()
		if !status.CheckedAt.IsZero() && !status.Updating {
			if status.CheckError != "" || !status.LastCheckChanged || status.NextCheckAt.Sub(status.CheckedAt) < catalogCheckInterval {
				t.Fatalf("automatic success did not reschedule: %+v", status)
			}
			if len(status.History) != 1 || !status.History[0].Automatic || !status.History[0].Changed {
				t.Fatalf("automatic update missing from history: %+v", status.History)
			}
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("automatic check did not finish")
		}
	}
}
