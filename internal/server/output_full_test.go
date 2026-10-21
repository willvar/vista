package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"vista/internal/store"
)

func TestFullOutputRoutesReadSavedUnicodeSnapshotsWithoutToken(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const session = "00000000-0000-4000-8000-000000000051"
	const otherSession = "00000000-0000-4000-8000-000000000052"
	const run = "00000000-0000-4000-8000-000000000053"
	const artifactID = "00000000-0000-4000-8000-000000000054"
	const jobID = "00000000-0000-4000-8000-000000000055"
	for _, id := range []string{session, otherSession} {
		if _, err := st.Create(ctx, id, "output"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := st.BeginRun(ctx, session, run, "input", "test"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	fragment := strings.Repeat("标题🙂\n", 180)
	content := strings.Repeat(fragment, 8)
	artifactPath := filepath.Join(root, "outputs", "artifact.log")
	jobPath := filepath.Join(root, "outputs", "job.log")
	for _, path := range []string{artifactPath, jobPath} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	artifact := store.Artifact{ID: artifactID, SessionID: session, RunID: run, OutputPath: artifactPath, Status: "started"}
	if _, err := st.RecordArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	artifact.Status, artifact.Complete, artifact.Bytes = "succeeded", true, int64(len(content))
	if _, err := st.RecordArtifact(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	job := store.Job{ID: jobID, SessionID: session, RunID: run, OutputPath: jobPath, Status: "starting", ExpiresAt: "2026-10-18T11:00:00Z"}
	if _, err := st.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.Status = "running"
	if _, err := st.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if _, err := st.AppendModelDelta(ctx, session, run, 300, 0, fragment); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{store: st, dir: root}
	handler := app.handler()
	request := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		input := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5186"+path, nil)
		handler.ServeHTTP(response, input)
		return response
	}
	// List and single-job reads report the growing file size, including jobs
	// with no byte-count event yet, and preserve ownership and lifetime data.
	response := request("/api/sessions/" + session + "/jobs/" + jobID)
	var jobView store.Job
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &jobView) != nil || jobView.Bytes != int64(len(content)) || jobView.ExpiresAt != job.ExpiresAt {
		t.Fatalf("single-job view: HTTP %d %+v", response.Code, jobView)
	}
	if response := request("/api/sessions/" + otherSession + "/jobs/" + jobID); response.Code != 404 {
		t.Fatalf("cross-session job metadata: %d", response.Code)
	}
	response = request("/api/sessions/" + session + "/jobs")
	var jobs []store.Job
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &jobs) != nil || len(jobs) != 1 || jobs[0].Bytes != int64(len(content)) {
		t.Fatalf("live job list: HTTP %d %+v", response.Code, jobs)
	}
	type output struct {
		Offset, NextOffset, AvailableBytes int64
		Content                            string
		Complete                           bool
	}
	paths := []struct {
		path     string
		complete bool
	}{
		{"/api/sessions/" + session + "/artifacts/" + artifactID + "/output?full=1", true},
		{"/api/sessions/" + session + "/jobs/" + jobID + "/output?full=1", false},
		{"/api/sessions/" + session + "/runs/" + run + "/model-output?turn=300&full=1", false},
	}
	for _, item := range paths {
		response := request(item.path)
		var view output
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &view) != nil || view.Offset != 0 || view.NextOffset != int64(len(content)) || view.AvailableBytes != int64(len(content)) || view.Content != content || !utf8.ValidString(view.Content) || view.Complete != item.complete {
			t.Fatalf("full snapshot %s: HTTP %d bytes=%d next=%d available=%d complete=%t", item.path, response.Code, len(view.Content), view.NextOffset, view.AvailableBytes, view.Complete)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("output snapshot may be cached")
		}
		for _, query := range []string{"&offset=1", "&offset=-1", "&offset=invalid", "&offset=9223372036854775808"} {
			if response := request(item.path + query); response.Code != http.StatusBadRequest {
				t.Fatalf("invalid/conflicting snapshot %s: HTTP %d", query, response.Code)
			}
		}
		if response := request(strings.Replace(item.path, "full=1", "full=wrong", 1)); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid full mode: HTTP %d", response.Code)
		}
		if response := request(strings.Replace(item.path, session, otherSession, 1)); response.Code != http.StatusNotFound {
			t.Fatalf("cross-session output: HTTP %d", response.Code)
		}
		response = request(strings.Replace(item.path, "full=1", "offset=0", 1))
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &view) != nil || view.NextOffset >= view.AvailableBytes || view.NextOffset > 4096 || !utf8.ValidString(view.Content) {
			t.Fatalf("default paging changed: HTTP %d bytes=%d next=%d", response.Code, len(view.Content), view.NextOffset)
		}
	}
	// The job's DB byte count is still zero, but its saved file keeps growing.
	// A new full request reads the new file snapshot instead of stale metadata.
	if err := os.WriteFile(jobPath, []byte(content+"追加🙂"), 0600); err != nil {
		t.Fatal(err)
	}
	var updated output
	response = request(paths[1].path)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &updated) != nil || updated.Content != content+"追加🙂" || updated.NextOffset != updated.AvailableBytes || updated.Complete {
		t.Fatalf("updated running snapshot: HTTP %d bytes=%d complete=%t", response.Code, len(updated.Content), updated.Complete)
	}
	if response := request(strings.Replace(paths[2].path, run, "00000000-0000-4000-8000-000000000059", 1)); response.Code != http.StatusNotFound {
		t.Fatalf("unknown run output: HTTP %d", response.Code)
	}
}

func TestFullOutputSnapshotKeepsPathIsolationAndValidUTF8(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "outputs", "running.log")
	content := append([]byte(strings.Repeat("🙂", 1500)), 0xf0, 0x9f)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{dir: root}
	response := httptest.NewRecorder()
	app.outputPage(response, httptest.NewRequest(http.MethodGet, "/output?full=1", nil), "output", path, false)
	var output struct {
		Content                    string
		NextOffset, AvailableBytes int64
		Complete                   bool
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &output) != nil || !utf8.ValidString(output.Content) || output.NextOffset != int64(len(content)) || output.AvailableBytes != int64(len(content)) || output.Complete {
		t.Fatalf("partial UTF-8 snapshot: HTTP %d bytes=%d", response.Code, len(output.Content))
	}
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outputs", "link.log")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{secret, link} {
		response := httptest.NewRecorder()
		app.outputPage(response, httptest.NewRequest(http.MethodGet, "/output?full=1", nil), "output", path, true)
		if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "private") {
			t.Fatalf("full output escaped root: HTTP %d", response.Code)
		}
	}
}
