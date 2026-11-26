package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"vista/internal/store"
)

func revertApp(t *testing.T) (*App, store.Message) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	const session = "00000000-0000-4000-8000-000000000044"
	s.Create(ctx, session, "rollback")
	message, _, err := s.BeginRunWithImages(ctx, session, "run", "00000000-0000-4000-8000-000000000045", "original text", []store.Image{{Name: "original.png", MediaType: "image/png", Data: testPNG(t)}})
	if err != nil {
		t.Fatal(err)
	}
	s.FinishRun(ctx, session, "run", "reply", "succeeded", "answer")
	s.SetOpen(ctx, session, false)
	return &App{store: s, dir: dir, ctx: ctx, workers: map[string]*worker{}}, message
}
func TestMessageDraftReadHasNoExecutionEffects(t *testing.T) {
	a, message := revertApp(t)
	draft, err := a.messageDraft(context.Background(), message.SessionID, message.ID)
	if err != nil || draft.Message.Content != "original text" || len(draft.Images) != 1 || draft.Images[0].Name != "original.png" {
		t.Fatal(draft, err)
	}
	messages, _ := a.store.MessagesAfter(context.Background(), message.SessionID, 0, 100)
	if len(messages) != 2 {
		t.Fatal("entering edit changed history")
	}
}
func TestRevertEndpointRetainsFileChangesAndRestoresDraftResourceBoundary(t *testing.T) {
	a, message := revertApp(t)
	ctx := context.Background()
	enabled := true
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{Branching: &enabled}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for name, content := range map[string]string{"modified": "agent changes", "added": "created", "external": "user changes"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0640); err != nil {
			t.Fatal(err)
		}
	}
	deleted := filepath.Join(directory, "deleted")
	if err := os.WriteFile(deleted, []byte("removed"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.session(w, httptest.NewRequest(http.MethodPost, "/api/sessions/"+message.SessionID+"/messages/"+message.ID+"/revert", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result revertedConversation
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Message.Content != message.Content || len(result.Images) != 1 || result.Images[0].Name != "original.png" || result.Images[0].Hash == "" {
		t.Fatal("draft lost", result)
	}
	for name, content := range map[string]string{"modified": "agent changes", "added": "created", "external": "user changes"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != content {
			t.Fatal("file changed during conversation rollback", name, string(data), err)
		}
	}
	if _, err := os.Stat(deleted); !os.IsNotExist(err) {
		t.Fatal("deleted file was recreated", err)
	}
	if result.Session == nil || result.Session.ID == message.SessionID {
		t.Fatal("missing branch", result)
	}
	messages, err := a.store.MessagesAfter(ctx, message.SessionID, 0, 100)
	if err != nil || len(messages) != 2 || messages[0].Content != message.Content {
		t.Fatal("source history was changed", messages, err)
	}
	images, err := a.store.SessionResources(ctx, message.SessionID, "", 100)
	if err != nil || len(images) != 1 || images[0].Hash != result.Images[0].Hash {
		t.Fatal("source attachments were lost", images, err)
	}
	messages, err = a.store.MessagesAfter(ctx, result.Session.ID, 0, 100)
	if err != nil || len(messages) != 0 {
		t.Fatal("source suffix leaked into branch", messages, err)
	}
	images, err = a.store.SessionResources(ctx, result.Session.ID, "", 100)
	if err != nil || len(images) != 0 {
		t.Fatal("source attachments leaked into branch context", images, err)
	}
}

func TestBranchPreferencePublishesWorkspaceSnapshot(t *testing.T) {
	a, message := revertApp(t)
	ctx := context.Background()
	root, child := message.SessionID, uuid.NewString()
	if err := a.store.SetOpen(ctx, root, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.Fork(ctx, root, child, message.ID); err != nil {
		t.Fatal(err)
	}
	put := func(id string) int {
		w := httptest.NewRecorder()
		a.session(w, httptest.NewRequest(http.MethodPut, "/api/sessions/"+id+"/branch-preference", nil))
		return w.Code
	}
	before := a.updates.snapshot()
	if status := put(child); status != http.StatusNoContent {
		t.Fatalf("save preference status: %d", status)
	}
	point, err := a.stateUpdateScoped(ctx, "", before, map[string]bool{"sessions": true}, nil)
	if err != nil || point.Revisions["sessions"] != before.Revisions["sessions"]+1 || point.Sessions == nil ||
		len(point.Sessions.Items) != 1 || point.Sessions.Items[0].BranchGroup == nil || point.Sessions.Items[0].BranchGroup.EntryID != child {
		t.Fatalf("preference not published in list snapshot: %+v, %v", point, err)
	}
	if status := put(child); status != http.StatusNoContent || a.updates.snapshot().Revisions["sessions"] != point.Revisions["sessions"] {
		t.Fatalf("identical write must be a no-op: %d", status)
	}
	if err := a.store.SetOpen(ctx, child, false); err != nil {
		t.Fatal(err)
	}
	if status := put(child); status != http.StatusConflict {
		t.Fatalf("closed branch write status: %d", status)
	}
	if status := put(uuid.NewString()); status != http.StatusNotFound {
		t.Fatalf("unknown branch write status: %d", status)
	}
}

func TestSameSessionRevertRequiresTheDisplayedPolicy(t *testing.T) {
	a, message := revertApp(t)
	ctx := t.Context()
	if err := a.store.SetOpen(ctx, message.SessionID, true); err != nil {
		t.Fatal(err)
	}
	base := "/api/sessions/" + message.SessionID
	path := base + "/messages/" + message.ID + "/revert"
	post := func(body []byte, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		a.session(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		if w.Code != want {
			t.Fatalf("revert: got %d, want %d: %s", w.Code, want, w.Body.String())
		}
		return w
	}
	post(nil, http.StatusConflict)
	preview, err := a.messageDraft(ctx, message.SessionID, message.ID)
	if err != nil || preview.WantsBranch() {
		t.Fatalf("default draft: %+v %v", preview, err)
	}
	body, err := json.Marshal(preview.HistoryChange)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{Branching: &enabled}); err != nil {
		t.Fatal(err)
	}
	post(body, http.StatusConflict)
	if list, err := a.store.HistoryVersions(ctx, message.SessionID, 0); err != nil || len(list.Items) != 0 {
		t.Fatalf("rejected operation saved a version: %+v %v", list, err)
	}
	enabled = false
	if _, err := a.store.PatchPreferences(ctx, store.PreferencesPatch{Branching: &enabled}); err != nil {
		t.Fatal(err)
	}
	w := post(body, http.StatusOK)
	var result revertedConversation
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Session != nil || result.Message.ID != message.ID || len(result.Images) != 1 {
		t.Fatalf("wrong same-session draft: %+v %v", result, err)
	}
	if _, err := a.store.SessionResource(ctx, message.SessionID, result.Images[0].Hash); err != nil {
		t.Fatal(err)
	}
	if branches, err := a.store.Branches(ctx, message.SessionID); err != nil || len(branches) != 1 {
		t.Fatalf("unexpected fork: %+v %v", branches, err)
	}
	point, err := a.store.Checkpoint(ctx, message.SessionID)
	if err != nil || !point.HasHistoryVersions || point.Session.Executing {
		t.Fatalf("wrong checkpoint: %+v %v", point, err)
	}
	// The withdrawn message is preserved but no longer an active target.
	post(body, http.StatusNotFound)
	if versions, err := a.store.HistoryVersions(ctx, message.SessionID, 0); err != nil || len(versions.Items) != 1 {
		t.Fatalf("repeated withdrawal changed saved versions: %+v %v", versions, err)
	}
}
