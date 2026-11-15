package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	"vista/internal/configstore"
	"vista/internal/network"
)

func TestLocalHTTPRequestsDoNotReadNetworkFiles(t *testing.T) {
	app := networkTestApp(t)
	settings := network.Settings{Profiles: []network.Profile{networkTestProfile()}, GlobalProxy: "profile"}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := configstore.Write(app.configDir, configstore.Network, data); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	for _, name := range []string{configstore.Filename, configstore.KeyFilename} {
		if _, err := unix.InotifyAddWatch(fd, filepath.Join(app.configDir, name), unix.IN_OPEN|unix.IN_ACCESS); err != nil {
			t.Fatal(err)
		}
	}
	handler := app.handler()
	for _, path := range []string{"/", "/assets/does-not-exist.js", "/licenses", "/api/port-ready.svg"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK && response.Code != http.StatusNotFound {
			t.Fatalf("local request %s failed: %d", path, response.Code)
		}
	}
	var events [4096]byte
	if count, err := unix.Read(fd, events[:]); count > 0 || !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("local HTTP requests opened/read network configuration or credentials: eventBytes=%d err=%v", count, err)
	}
	// A real policy selection must still read and hydrate the same files.
	app.networkRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy, err := network.FromContext(r.Context())
		if err != nil || policy.Profile == nil || policy.Profile.Password != settings.Profiles[0].Password {
			t.Fatalf("operation did not load proxy credentials: %+v %v", policy, err)
		}
		if count, err := unix.Read(fd, events[:]); count <= 0 || err != nil {
			t.Fatalf("file-read observation did not detect policy capture: eventBytes=%d err=%v", count, err)
		}
		if _, err := network.ForConnection(r.Context(), "profile"); err != nil {
			t.Fatal(err)
		}
		if _, err := network.FromContext(r.Context()); err != nil {
			t.Fatal(err)
		}
		if count, err := unix.Read(fd, events[:]); count > 0 || !errors.Is(err, unix.EAGAIN) {
			t.Fatalf("repeated selections reread network files: eventBytes=%d err=%v", count, err)
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/outbound-operation", nil))
}
