package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"vista/internal/store"
)

func TestConversationGroupLifecycle(t *testing.T) {
	a, first := revertApp(t) // Closed root with canonical history.
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	root, child, grandchild, other := first.SessionID, uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := a.store.Fork(ctx, root, child, first.ID)
	must(err)
	must(a.store.SetOpen(ctx, child, true))
	message, _, err := a.store.BeginRun(ctx, child, "derived-run", uuid.NewString(), "derived input")
	must(err)
	_, err = a.store.FinishRun(ctx, child, "derived-run", uuid.NewString(), "succeeded", "derived answer")
	must(err)
	oldMembers, err := a.store.ConversationMembers(ctx, child)
	must(err)
	_, err = a.store.Fork(ctx, child, grandchild, message.ID)
	must(err)
	must(a.store.SetOpen(ctx, grandchild, false))
	_, err = a.store.Create(ctx, other, "outside the group")
	must(err)

	// A new fork that raced the initial membership read makes that scope stale.
	if err := a.store.CloseConversation(ctx, child, oldMembers); !errors.Is(err, store.ErrConversationChanged) {
		t.Fatal(err)
	}
	if _, err := a.store.DeleteConversation(ctx, child, oldMembers); !errors.Is(err, store.ErrConversationChanged) {
		t.Fatal(err)
	}
	before, err := a.store.Get(ctx, child)
	must(err)
	if !before.Open {
		t.Fatal("stale operation closed a member")
	}

	// Save an owned output and an output still referenced outside this group.
	owned := filepath.Join(a.dir, "outputs", "owned.log")
	shared := filepath.Join(a.dir, "outputs", "shared.log")
	must(os.MkdirAll(filepath.Dir(owned), 0700))
	for _, owner := range []string{child, other} {
		run := uuid.NewString()
		_, _, err := a.store.BeginRun(ctx, owner, run, uuid.NewString(), "output fixture")
		must(err)
		paths := []string{shared}
		if owner == child {
			paths = append(paths, owned)
		}
		for _, path := range paths {
			must(os.WriteFile(path, []byte("output"), 0600))
			artifact := store.Artifact{ID: uuid.NewString(), SessionID: owner, RunID: run, Status: "started", OutputPath: path}
			_, err = a.store.RecordArtifact(ctx, artifact)
			must(err)
			artifact.Status = "succeeded"
			_, err = a.store.RecordArtifact(ctx, artifact)
			must(err)
		}
		_, err = a.store.FinishRun(ctx, owner, run, uuid.NewString(), "succeeded", "saved output")
		must(err)
	}

	members := []string{root, child, grandchild}
	slices.Sort(members)
	flow := a.stateMutations(http.HandlerFunc(a.session))
	request := func(method, path string, expected int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		flow.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != expected {
			t.Fatalf("%s %s: HTTP %d, %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	// Existing branch-specific API retains its explicit scope and safety guard.
	request(http.MethodDelete, "/api/sessions/"+root, http.StatusConflict)
	assertGroup := func(method, path string) {
		t.Helper()
		before := a.updates.snapshot().Revisions["sessions"]
		w := request(method, path, http.StatusOK)
		var out struct {
			SessionIDs []string `json:"sessionIds"`
		}
		must(json.Unmarshal(w.Body.Bytes(), &out))
		if !slices.Equal(out.SessionIDs, members) {
			t.Fatalf("got affected set %v, want %v", out.SessionIDs, members)
		}
		if a.updates.snapshot().Revisions["sessions"] != before+1 {
			t.Fatal("expected one committed workspace change")
		}
	}
	notify, release, ok := a.hub.subscribe(grandchild)
	if !ok {
		t.Fatal("could not observe the branch")
	}
	defer release()
	assertNotified := func() {
		t.Helper()
		select {
		case <-notify:
		default:
			t.Fatal("branch subscribers weren't notified")
		}
	}
	assertGroup(http.MethodPost, "/api/sessions/"+child+"/group/close")
	assertNotified()
	for _, id := range members {
		stored, err := a.store.Get(ctx, id)
		must(err)
		if stored.Open {
			t.Fatal("group member was left open", id)
		}
	}
	history, err := a.store.MessagesAfter(ctx, child, 0, 100)
	must(err)
	if len(history) != 4 || history[0].ID != message.ID {
		t.Fatalf("closing changed history: %v", history)
	}
	assertGroup(http.MethodDelete, "/api/sessions/"+grandchild+"/group")
	assertNotified()
	for _, id := range members {
		if _, err := a.store.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("group member was not deleted", id, err)
		}
	}
	if _, err := os.Stat(owned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced output survived: %v", err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("shared output was lost: %v", err)
	}
	unrelated, err := a.store.Get(ctx, other)
	must(err)
	if !unrelated.Open {
		t.Fatal("unrelated conversation was affected")
	}
}
