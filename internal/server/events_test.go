package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"vista/internal/store"
)

func TestEventHubBoundsSlowSubscribersAndReleasesSlots(t *testing.T) {
	app := &App{}
	hub := &app.hub
	unsubscribe := make([]func(), 0, maxSessionEventStreams)
	for range maxSessionEventStreams {
		_, leave, ok := hub.subscribe("one")
		if !ok {
			t.Fatal("Session event stream rejected before its limit")
		}
		unsubscribe = append(unsubscribe, leave)
	}
	if _, _, ok := hub.subscribe("one"); ok {
		t.Fatal("Session event stream limit was not enforced")
	}
	w := httptest.NewRecorder()
	app.eventStream(w, httptest.NewRequest(http.MethodGet, "/events", nil), "one")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("excess event stream response: %d", w.Code)
	}
	unsubscribe[0]()
	unsubscribe[0]() // A second cleanup must not free another subscriber's slot.
	_, leave, ok := hub.subscribe("one")
	if !ok || hub.subscribers != maxSessionEventStreams {
		t.Fatalf("Session slot was not reclaimed: subscribers=%d", hub.subscribers)
	}
	leave()
	for _, done := range unsubscribe[1:] {
		done()
	}
	if hub.subscribers != 0 || len(hub.clients) != 0 {
		t.Fatalf("event subscriptions retained: count=%d, clients=%d", hub.subscribers, len(hub.clients))
	}
	for i := range maxEventStreams {
		_, _, ok := hub.subscribe(strconv.Itoa(i))
		if !ok {
			t.Fatalf("global event stream rejected early at %d", i)
		}
	}
	if _, _, ok := hub.subscribe("overflow"); ok {
		t.Fatal("global event stream limit was not enforced")
	}
}

func TestEventStreamDisconnectReleasesSubscriber(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &App{store: st, ctx: ctx}
	finished := make(chan struct{})
	requestCtx, disconnect := context.WithCancel(context.Background())
	w := httptest.NewRecorder()
	go func() {
		defer close(finished)
		app.eventStream(w, httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(requestCtx), "one")
	}()
	deadline := time.After(2 * time.Second)
	for {
		app.hub.mu.Lock()
		count := app.hub.subscribers
		app.hub.mu.Unlock()
		if count == 1 {
			break
		}
		select {
		case <-finished:
			t.Fatal("event stream finished before it subscribed")
		case <-deadline:
			t.Fatal("event stream did not subscribe")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	disconnect()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("event stream did not stop after disconnect")
	}
	app.hub.mu.Lock()
	remaining := app.hub.subscribers
	app.hub.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("event stream subscriber retained after disconnect: %d", remaining)
	}
}
