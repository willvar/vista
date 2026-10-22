package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"vista/internal/buildinfo"
	"vista/internal/instance"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func resetFixture(t *testing.T) (*appHandler, http.Handler) {
	t.Helper()
	original := provider.CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	config := filepath.Join(t.TempDir(), "vista")
	state := filepath.Join(config, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	open := func() (*App, error) { return loadApp(ctx, cancel, config, state, buildinfo.Metadata{}) }
	app, err := open()
	if err != nil {
		t.Fatal(err)
	}
	handler := newAppHandler(app, open)
	t.Cleanup(handler.close)
	return handler, app.host(handler)
}

func resetCall(handler http.Handler, method, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, "http://127.0.0.1:5186/api/reset", strings.NewReader(body)))
	return w
}

func writeResetFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestResetAllReopensEmptyDataAndKeepsInstanceLock(t *testing.T) {
	for _, key := range []string{"VISTA_OPENAI_API_KEY", "VISTA_ANTHROPIC_API_KEY", "VISTA_GEMINI_API_KEY", "VISTA_MISTRAL_API_KEY", "VISTA_DEFAULT_MODEL"} {
		t.Setenv(key, "")
	}
	h, handler := resetFixture(t)
	app := h.app
	ctx := context.Background()
	lock, err := instance.Lock(app.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Unlock(lock)
	info := instance.Info{PID: os.Getpid(), Address: "http://127.0.0.1:5186"}
	if err := instance.Write(app.dir, info); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Create(ctx, "old-session", "old conversation"); err != nil {
		t.Fatal(err)
	}
	if err := app.store.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: "old preference"}); err != nil {
		t.Fatal(err)
	}
	oldPreferences, err := app.store.Preferences(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.store.BeginRunWithImages(ctx, "old-session", "old-run", "image", "attached", []store.Image{{MediaType: "image/png", Data: testPNG(t)}}); err != nil {
		t.Fatal(err)
	}
	paths := []string{"models.json", "codex-auth/account.json", "mcp.json", "mcp-auth/token.json", "AGENTS.md", "skills/example/SKILL.md", "agent-settings.json", "search.json", "user-agent.json", "server.json", "model-catalog-status.json", ".mcp-login-old/token.json", "state/outputs/old", "state/worker.log", "future-setting.json"}
	for _, path := range paths {
		writeResetFixture(t, filepath.Join(app.configDir, path), "old private data")
	}
	logPath := filepath.Join(app.dir, "server.log")
	writeResetFixture(t, logPath, "old log contents")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	result := resetCall(handler, http.MethodPost, `{"confirm":"reset-all"}`)
	if result.Code != http.StatusOK || result.Body.String() != "{\"reset\":true}\n" {
		t.Fatalf("reset: %d %s", result.Code, result.Body.String())
	}
	if h.app == app || app.ctx.Err() == nil {
		t.Fatal("old in-memory state was retained")
	}
	if catalog := h.app.modelsSnapshot(); catalog.Default != "" || len(catalog.Models) != 0 {
		t.Fatalf("reset retained an unconfigured model: %+v", catalog)
	}
	for _, path := range paths {
		// Some defaults (such as search ordering) are freshly generated on load.
		if contents, err := os.ReadFile(filepath.Join(app.configDir, path)); !errors.Is(err, os.ErrNotExist) && (err != nil || strings.Contains(string(contents), "old private data")) {
			t.Fatalf("private data survived: %s (%v)", path, err)
		}
	}
	counts, err := h.app.store.SessionCounts(ctx)
	if err != nil || counts.Total != 0 || counts.Jobs != 0 {
		t.Fatalf("conversations survived reset: %+v %v", counts, err)
	}
	memories, err := h.app.store.Memories(ctx)
	if err != nil || len(memories) != 0 {
		t.Fatalf("memory survived reset: %+v %v", memories, err)
	}
	resources, err := h.app.store.ResourceStorage(ctx)
	if err != nil || resources.TotalCount != 0 {
		t.Fatalf("resource originals survived reset: %+v %v", resources, err)
	}
	preferences, err := h.app.store.Preferences(ctx)
	if err != nil || preferences.UserID == oldPreferences.UserID {
		t.Fatalf("preferences were retained: %+v %v", preferences, err)
	}
	if got, err := instance.Read(app.dir); err != nil || got != info {
		t.Fatalf("instance address changed: %+v %v", got, err)
	}
	if other, err := instance.Lock(app.dir); err == nil {
		instance.Unlock(other)
		t.Fatal("instance lock was replaced during reset")
	}
	if contents, err := os.ReadFile(logPath); err != nil || len(contents) != 0 {
		t.Fatalf("server log was not cleared: %q %v", contents, err)
	}
	if _, err := logFile.WriteString("new generation"); err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(logPath); string(contents) != "new generation" {
		t.Fatalf("open log handle was detached from its path: %q", contents)
	}
	for _, path := range []string{"/api/status", "/api/models", "/api/sessions", "/api/agent-settings", "/api/mcp-config", "/api/search-config", "/api/user-agent", "/api/skills", "/api/memories"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5186"+path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("fresh instance unavailable: %s %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestResetRequiresConfirmationAndSameOrigin(t *testing.T) {
	h, handler := resetFixture(t)
	path := filepath.Join(h.app.configDir, "AGENTS.md")
	writeResetFixture(t, path, "keep")
	for _, body := range []string{"", `{}`, `null`, `{"confirm":true}`, `{"confirm":"reset"}`, `{"confirm":"reset-all","extra":true}`, `{"confirm":"reset-all"}{}`, `{"confirm":"` + strings.Repeat("x", 300) + `"}`} {
		if result := resetCall(handler, http.MethodPost, body); result.Code != http.StatusBadRequest {
			t.Fatalf("invalid confirmation accepted: %d %s", result.Code, result.Body.String())
		}
	}
	if result := resetCall(handler, http.MethodGet, ""); result.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET reset: %d", result.Code)
	}
	for _, input := range []struct{ host, origin string }{{"evil.example", ""}, {"127.0.0.1:5186", "https://evil.example"}} {
		r := httptest.NewRequest(http.MethodPost, "http://"+input.host+"/api/reset", strings.NewReader(`{"confirm":"reset-all"}`))
		r.Header.Set("Origin", input.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("foreign reset accepted: %d", w.Code)
		}
	}
	if contents, err := os.ReadFile(path); err != nil || string(contents) != "keep" {
		t.Fatalf("unconfirmed reset touched data: %q %v", contents, err)
	}
}

func TestResetDrainsRequestsAndBackgroundWriters(t *testing.T) {
	h, handler := resetFixture(t)
	old := h.app
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	requestFinished := make(chan struct{})
	lateWriter := make(chan struct{})
	// Simulate an HTTP save which finishes after its context is canceled.
	h.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		<-release
		_ = os.WriteFile(filepath.Join(old.configDir, "late-save.json"), []byte("old secret"), 0600)
	})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "http://127.0.0.1:5186/api/test-save", nil))
		close(requestFinished)
	}()
	<-started
	old.background.Go(func() {
		<-old.ctx.Done()
		<-release
		_ = os.WriteFile(filepath.Join(old.configDir, "late-login.json"), []byte("old credential"), 0600)
		close(lateWriter)
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- resetCall(handler, http.MethodPost, `{"confirm":"reset-all"}`) }()
	select {
	case <-old.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("reset did not cancel active work")
	}
	if result := resetCall(handler, http.MethodPost, `{"confirm":"reset-all"}`); result.Code != http.StatusConflict {
		t.Fatalf("concurrent reset accepted: %d", result.Code)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "http://127.0.0.1:5186/api/agent-settings", strings.NewReader(`{"instructions":"new old value"}`)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("save was not blocked during reset: %d", w.Code)
	}
	select {
	case <-done:
		t.Fatal("reset finished before old writers drained")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-done:
		if result.Code != http.StatusOK {
			t.Fatalf("reset: %d %s", result.Code, result.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reset did not finish after writers drained")
	}
	<-requestFinished
	<-lateWriter
	for _, name := range []string{"late-save.json", "late-login.json"} {
		if _, err := os.Stat(filepath.Join(old.configDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("late writer restored deleted data: %s %v", name, err)
		}
	}
}

func TestResetReopenFailureIsVisibleAndRetryable(t *testing.T) {
	h, handler := resetFixture(t)
	open := h.open
	h.open = func() (*App, error) { return nil, errors.New("test reopen failure") }
	result := resetCall(handler, http.MethodPost, `{"confirm":"reset-all"}`)
	if result.Code != http.StatusInternalServerError || !strings.Contains(result.Body.String(), "test reopen failure") {
		t.Fatalf("reopen failure hidden: %d %s", result.Code, result.Body.String())
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5186/api/models", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed generation was reused after failure: %d", w.Code)
	}
	h.open = open
	if result := resetCall(handler, http.MethodPost, `{"confirm":"reset-all"}`); result.Code != http.StatusOK {
		t.Fatalf("retry failed: %d %s", result.Code, result.Body.String())
	}
}

func TestResetDoesNotFollowExternalSymlinks(t *testing.T) {
	config := t.TempDir()
	state := filepath.Join(config, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	outside := filepath.Join(external, "SKILL.md")
	writeResetFixture(t, outside, "external skill")
	if err := os.Symlink(external, filepath.Join(config, "skills")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := clearUserData(config, state); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(outside); err != nil || string(contents) != "external skill" {
		t.Fatalf("reset followed external skill link: %q %v", contents, err)
	}
	if err := clearUserData(config, external); err == nil {
		t.Fatal("reset accepted a state path outside Vista")
	}
}
