package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	modelcfg "vista/internal/model"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func setTestInterests(t *testing.T, a *App, id string, topics ...string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id, "topics": topics, "timezone": "Asia/Kathmandu"})
	w := httptest.NewRecorder()
	a.stateInterests(w, httptest.NewRequest(http.MethodPut, "/api/events/subscriptions", strings.NewReader(string(body))))
	if w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestViewInterestsAreScopedToLiveStreamAndBounded(t *testing.T) {
	a := &App{}
	client, _, leave, _ := a.updates.subscribeClient()
	setTestInterests(t, a, client.id, "statistics", "storage")
	if !a.updates.interested("storage") || a.updates.interested("skills") {
		t.Fatal("interests not scoped")
	}
	_, zone, _ := a.updates.clientInterests(client.id)
	if zone == nil || zone.String() != "Asia/Kathmandu" {
		t.Fatal("lost viewer timezone")
	}
	setTestInterests(t, a, client.id, "models")
	if a.updates.interested("statistics") || a.updates.interested("storage") {
		t.Fatal("closed views retained")
	}
	for _, body := range []string{
		fmt.Sprintf(`{"id":%q,"topics":["unknown"]}`, client.id),
		fmt.Sprintf(`{"id":%q,"topics":["statistics"],"timezone":"invalid"}`, client.id),
		fmt.Sprintf(`{"id":%q,"topics":["storage"],"secret":true}`, client.id),
	} {
		w := httptest.NewRecorder()
		a.stateInterests(w, httptest.NewRequest("PUT", "/", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatal("accepted invalid interests", body, w.Code)
		}
	}
	setTestInterests(t, a, client.id, "storage")
	leave()
	if a.updates.interested("storage") {
		t.Fatal("stream disconnection leaked interests")
	}
	w := httptest.NewRecorder()
	a.stateInterests(w, httptest.NewRequest("PUT", "/", strings.NewReader(fmt.Sprintf(`{"id":%q,"topics":[]}`, client.id))))
	if w.Code != http.StatusNotFound {
		t.Fatal("expired stream accepted", w.Code)
	}
}

func TestWorkerObservationPublishesAfterCommit(t *testing.T) {
	c, _ := newResultConsumerTest(t)
	a := &App{store: c.store}
	c.changed = func(kind string) {
		if kind != "call_usage" {
			t.Fatal(kind)
		}
		groups, _, err := c.store.ConnectionUsage(c.ctx, "connection:fixture", 30, time.Now())
		if err != nil || len(groups) != 1 || groups[0].FirstTokenSamples != 1 {
			t.Fatalf("notification preceded commit: %+v %v", groups, err)
		}
		a.notifyWorkerState(kind)
	}
	result := workerproto.Result{Kind: "call_usage", CallUsage: &modelcfg.CallUsage{ID: uuid.NewString(), Connection: "connection:fixture", Model: "fixture", Route: strings.Repeat("a", 64), FirstTokenMS: new(100.0), Outcome: "succeeded"}}
	if err := c.consume(result); err != nil {
		t.Fatal(err)
	}
	if a.updates.snapshot().Revisions["connection-statistics"] != 1 {
		t.Fatal("missing committed usage notification")
	}
	result.CallUsage.Route = "bad"
	if err := c.consume(result); err == nil {
		t.Fatal("invalid sample accepted")
	}
	if a.updates.snapshot().Revisions["connection-statistics"] != 1 {
		t.Fatal("invalid sample notified")
	}
}

func TestUnsubscribedViewsDoNotLoadOrPushTheirSnapshots(t *testing.T) {
	c, _ := newResultConsumerTest(t)
	a := &App{store: c.store, storageStatus: resourceStorageStatus{Phase: "idle", Stats: &store.ResourceStorage{TotalCount: 1}}}
	before := a.updates.snapshot()
	a.updates.notify("storage", "diagnostics", "skills", "connection-statistics")
	point, err := a.stateUpdateScoped(c.ctx, "localhost", before, map[string]bool{"models": true}, nil)
	if err != nil || point.Storage != nil || point.Diagnostics != nil || point.Revisions["storage"] != 1 {
		t.Fatal(point, err)
	}
	point, err = a.stateUpdateScoped(c.ctx, "localhost", before, map[string]bool{"storage": true}, nil)
	if err != nil || point.Storage == nil || point.Storage.Stats.TotalCount != 1 {
		t.Fatal(point, err)
	}
}

func TestStatisticsBoundariesUseViewerTimeIncludingDST(t *testing.T) {
	for _, tc := range []struct{ zone, now, next string }{
		{"Asia/Kathmandu", "2026-11-04T00:10:00Z", "2026-11-04T00:15:00Z"},
		{"Asia/Hong_Kong", "2026-11-03T15:59:59Z", "2026-11-03T16:00:00Z"},
		{"America/New_York", "2026-03-08T06:59:59Z", "2026-03-08T07:00:00Z"},
		{"America/New_York", "2026-11-01T05:59:59Z", "2026-11-01T06:00:00Z"},
		{"UTC", "2026-11-04T01:00:00Z", "2026-11-04T02:00:00Z"},
	} {
		zone, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		now, _ := time.Parse(time.RFC3339, tc.now)
		if got := nextStatisticsBoundary(now, zone).UTC().Format(time.RFC3339); got != tc.next {
			t.Fatal(tc, got)
		}
	}
}

type stateFrameWriter struct {
	header http.Header
	frames chan []byte
}

func (w *stateFrameWriter) Header() http.Header { return w.header }
func (w *stateFrameWriter) WriteHeader(int)     {}
func (w *stateFrameWriter) Flush()              {}
func (w *stateFrameWriter) Write(data []byte) (int, error) {
	w.frames <- append([]byte(nil), data...)
	return len(data), nil
}

func TestOpenStatisticsViewUpdatesAtClockBoundaryAndStopsOnClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a := &App{ctx: ctx}
		w := &stateFrameWriter{header: http.Header{}, frames: make(chan []byte, 1024)}
		go a.stateEvents(w, httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx))
		var initial stateCheckpoint
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(strings.SplitN(string(<-w.frames), "data: ", 2)[1], "data: "))), &initial); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute * 2)
		if a.updates.snapshot().Revisions["statistics"] != 0 {
			t.Fatal("unopened statistics advanced")
		}
		setTestInterests(t, a, initial.SubscriptionID, "statistics")
		synctest.Wait()
		zone, _ := time.LoadLocation("Asia/Kathmandu")
		time.Sleep(time.Until(nextStatisticsBoundary(time.Now(), zone)) + time.Second)
		synctest.Wait()
		if a.updates.snapshot().Revisions["statistics"] != 1 {
			t.Fatal("hour boundary was not published")
		}
		setTestInterests(t, a, initial.SubscriptionID)
		synctest.Wait()
		time.Sleep(2 * time.Hour)
		if a.updates.snapshot().Revisions["statistics"] != 1 {
			t.Fatal("closed view kept its clock")
		}
		setTestInterests(t, a, initial.SubscriptionID, "connection-statistics")
		synctest.Wait()
		time.Sleep(time.Until(nextStatisticsBoundary(time.Now(), zone)) + time.Second)
		synctest.Wait()
		if a.updates.snapshot().Revisions["connection-statistics"] != 0 || a.updates.snapshot().Revisions["statistics"] != 1 {
			t.Fatal("connection statistics unexpectedly scheduled the global view clock")
		}
	})
}

func TestManagedSkillChangesAndDirectoryReplacementNotifyWithoutFollowingLinks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{ctx: ctx, configDir: t.TempDir(), dir: t.TempDir()}
	root := filepath.Join(a.configDir, "skills")
	if err := os.MkdirAll(filepath.Join(root, "example"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip(err)
	}
	done := make(chan struct{})
	go func() { a.watchManagedFiles(); close(done) }()
	defer func() { cancel(); <-done }()
	// Repeat writes until the OS watcher has installed its initial directory
	// watches; no test-only startup hook or production polling is required.
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("metadata"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "example", "SKILL.md")
	deadline := time.Now().Add(3 * time.Second)
	for a.updates.snapshot().Revisions["skills"] == 0 && time.Now().Before(deadline) {
		write(file)
		time.Sleep(60 * time.Millisecond)
	}
	if a.updates.snapshot().Revisions["skills"] == 0 {
		t.Fatal("external write not observed")
	}
	// Supporting files can change without touching the entrypoint. Include
	// newly created directories, not only folders present at watcher startup.
	nested := filepath.Join(root, "example", "references", "nested")
	nestedBefore := a.updates.snapshot().Revisions["skills"]
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for a.updates.snapshot().Revisions["skills"] == nestedBefore && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.updates.snapshot().Revisions["skills"] == nestedBefore {
		t.Fatal("new supporting directory not observed")
	}
	nestedBefore = a.updates.snapshot().Revisions["skills"]
	write(filepath.Join(nested, "new.md"))
	deadline = time.Now().Add(3 * time.Second)
	for a.updates.snapshot().Revisions["skills"] == nestedBefore && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.updates.snapshot().Revisions["skills"] == nestedBefore {
		t.Fatal("nested supporting file change not observed")
	}
	time.Sleep(250 * time.Millisecond)
	before := a.updates.snapshot().Revisions["skills"]
	write(filepath.Join(outside, "SKILL.md"))
	time.Sleep(250 * time.Millisecond)
	if a.updates.snapshot().Revisions["skills"] != before {
		t.Fatal("followed skill symlink")
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "replacement", "SKILL.md"))
	deadline = time.Now().Add(3 * time.Second)
	for a.updates.snapshot().Revisions["skills"] == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.updates.snapshot().Revisions["skills"] == before {
		t.Fatal("directory replacement not observed")
	}
}

func TestStorageChangesWaitForViewAndCoalesce(t *testing.T) {
	a, _ := inputQueueFixture(t)
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	a.storageWake = make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { a.syncResourceStorage(); close(done) }()
	defer func() { cancel(); <-done }()
	for range 10 {
		a.invalidateResourceStorage()
	}
	time.Sleep(300 * time.Millisecond)
	if a.resourceStorageSnapshot().Stats != nil {
		t.Fatal("unopened storage scanned")
	}
	client, _, leave, _ := a.updates.subscribeClient()
	defer leave()
	setTestInterests(t, a, client.id, "storage")
	deadline := time.Now().Add(3 * time.Second)
	for a.resourceStorageSnapshot().Stats == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if a.resourceStorageSnapshot().Stats == nil {
		t.Fatal("storage view did not trigger scan")
	}
	a.storageMu.Lock()
	scanned := a.storageScanned
	generation := a.storageGeneration
	a.storageMu.Unlock()
	if scanned != generation {
		t.Fatal("dirty generation lost")
	}
	sampled := a.resourceStorageSnapshot().Stats.ScannedAt
	setTestInterests(t, a, client.id)
	setTestInterests(t, a, client.id, "storage")
	time.Sleep(300 * time.Millisecond)
	if a.resourceStorageSnapshot().Stats.ScannedAt != sampled {
		t.Fatal("reopening rescanned unchanged storage")
	}
	setTestInterests(t, a, client.id)
	for range 10 {
		a.invalidateResourceStorage()
	}
	time.Sleep(300 * time.Millisecond)
	a.storageMu.Lock()
	next := a.storageScanned
	a.storageMu.Unlock()
	if scanned != next {
		t.Fatal("closed storage view kept scanning")
	}
}

func TestStorageChangeDuringScanSchedulesAnotherPass(t *testing.T) {
	a, _ := inputQueueFixture(t)
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	a.storageWake = make(chan struct{}, 1)
	client, _, leave, _ := a.updates.subscribeClient()
	defer leave()
	setTestInterests(t, a, client.id, "storage")
	// Hold the shared SQL connection: scans now use backend-native file
	// coordination rather than the former whole-library file lock.
	transaction := a.store.ResourceControl().DB.WithContext(ctx).Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	defer transaction.Rollback()
	scanStatus := func() resourceStorageStatus {
		a.storageMu.Lock()
		defer a.storageMu.Unlock()
		return a.storageStatus
	}
	done := make(chan struct{})
	go func() { a.syncResourceStorage(); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for scanStatus().Phase != "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if scanStatus().Phase != "running" {
		t.Fatal("automatic scan did not enter the blocked phase")
	}
	// The first scan is blocked on the database connection. A later
	// commit must retain its dirty generation after that scan completes.
	time.Sleep(30 * time.Millisecond)
	a.invalidateResourceStorage()
	transaction.Rollback()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.storageMu.Lock()
		caughtUp := a.storageScanned == a.storageGeneration && a.storageStatus.Phase == "idle"
		a.storageMu.Unlock()
		if caughtUp {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("in-scan invalidation was lost")
}

func TestClosingLastStorageViewCancelsBlockedAutomaticScan(t *testing.T) {
	a, _ := inputQueueFixture(t)
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	a.storageWake = make(chan struct{}, 1)
	client, _, leave, _ := a.updates.subscribeClient()
	defer leave()
	setTestInterests(t, a, client.id, "storage")
	// Hold the shared SQL connection: scans now use backend-native file
	// coordination rather than the former whole-library file lock.
	transaction := a.store.ResourceControl().DB.WithContext(ctx).Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	scanStatus := func() resourceStorageStatus {
		a.storageMu.Lock()
		defer a.storageMu.Unlock()
		return a.storageStatus
	}
	defer transaction.Rollback()
	done := make(chan struct{})
	go func() { a.syncResourceStorage(); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for scanStatus().Phase != "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if scanStatus().Phase != "running" {
		t.Fatal("automatic scan did not enter the blocked phase")
	}
	setTestInterests(t, a, client.id)
	deadline = time.Now().Add(3 * time.Second)
	for scanStatus().Phase == "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	status := scanStatus()
	if status.Phase != "idle" || status.Error != "" || status.Stats != nil {
		t.Fatal("last viewer did not cancel scan cleanly", status)
	}
}
