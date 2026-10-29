//go:build linux

package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"vista/internal/resources"
	"vista/internal/store"
	workerprocess "vista/internal/worker"
)

func TestResourceWorkerHelper(t *testing.T) {
	if os.Getenv("VISTA_RESOURCE_WORKER_HELPER") != "1" {
		return
	}
	if err := workerprocess.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
func TestImageUploadUsesRealWorkerAndBoundedReferences(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "vista")
	state := filepath.Join(config, "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	session := "00000000-0000-4000-8000-000000000091"
	if _, err = db.Create(context.Background(), session, "image"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &App{ctx: ctx, store: db, dir: state, configDir: config, workers: map[string]*worker{}}
	launch := func(ctx context.Context, id string) (*worker, *bufio.Scanner, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestResourceWorkerHelper$")
		cmd.Env = append(os.Environ(), "VISTA_RESOURCE_WORKER_HELPER=1", "XDG_CONFIG_HOME="+root)
		return app.launchWorkerCommand(ctx, id, cmd)
	}
	if err = app.startWorkerUsing(session, launch); err != nil {
		t.Fatal(err)
	}
	defer func() { app.stopAll(); app.background.Wait() }()
	// The Server only streams this 50 MiB original. Worker imports/hash-checks it;
	// neither the HTTP JSON response nor IPC carries the bytes.
	prefix := testPNG(t)
	data := io.MultiReader(bytes.NewReader(prefix), io.LimitReader(zeroImageReader{}, resources.MaxImageBytes-int64(len(prefix))))
	request := httptest.NewRequest(http.MethodPost, "/resources?name=original.png", data)
	request.Header.Set("Content-Type", "image/png")
	response := httptest.NewRecorder()
	app.uploadResource(response, request, session)
	var ref resources.Ref
	if response.Code != 200 || response.Body.Len() > 2048 || json.Unmarshal(response.Body.Bytes(), &ref) != nil || ref.Bytes != resources.MaxImageBytes {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	expected := sha256.New()
	io.Copy(expected, io.MultiReader(bytes.NewReader(prefix), io.LimitReader(zeroImageReader{}, resources.MaxImageBytes-int64(len(prefix)))))
	if ref.Hash != fmt.Sprintf("%x", expected.Sum(nil)) {
		t.Fatal("upload bytes changed")
	}
	if _, err = db.SessionResource(ctx, session, ref.Hash); err != nil {
		t.Fatal("resource returned before ownership commit", err)
	}
	entries, err := os.ReadDir(filepath.Join(state, "inputs"))
	if err != nil || len(entries) != 0 {
		t.Fatal("staged upload leaked", err)
	}
	// Capacity rejection happens before consuming or staging the request body.
	app.memoryConfig = memoryConfig{BudgetMiB: 256, WorkerMiB: 128, ConcurrentRuns: 1}
	app.mu.Lock()
	app.workers[session].metrics.RSSBytes = 256 << 20
	app.mu.Unlock()
	blocked := httptest.NewRecorder()
	blockedRequest := httptest.NewRequest(http.MethodPost, "/resources", bytes.NewReader(prefix))
	blockedRequest.Header.Set("Content-Type", "image/png")
	app.uploadResource(blocked, blockedRequest, session)
	if blocked.Code != 503 {
		t.Fatalf("exhausted capacity accepted upload: %d", blocked.Code)
	}
}

type zeroImageReader struct{}

func (zeroImageReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
