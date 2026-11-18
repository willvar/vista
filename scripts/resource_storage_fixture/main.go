// Resource-storage browser fixture. Payloads use the same bounded HTTP fixture
// as Go contract tests; this does not stand in for the live S3 integration test.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"vista/internal/resourcetest"
)

type owner struct{ cleanup []func() }

func (*owner) Helper()             {}
func (o *owner) Cleanup(fn func()) { o.cleanup = append(o.cleanup, fn) }
func main() {
	own := &owner{}
	s3 := resourcetest.NewS3(own)
	var mu sync.Mutex
	var gate chan struct{}
	blocked := 0
	deny := false
	release := func() {
		mu.Lock()
		defer mu.Unlock()
		if gate != nil {
			close(gate)
			gate = nil
		}
	}
	s3.SetHook(func(r *http.Request) int {
		if r.Method != http.MethodPut || (!strings.Contains(r.URL.Path, "/objects/") || strings.Contains(r.URL.Path, "/checks/")) {
			return 0
		}
		mu.Lock()
		wait := gate
		fail := deny
		if wait != nil {
			blocked++
		}
		mu.Unlock()
		if fail {
			return http.StatusForbidden
		}
		if wait != nil {
			select {
			case <-wait:
			case <-r.Context().Done():
			}
		}
		return 0
	})
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hold":
			mu.Lock()
			if gate == nil {
				gate = make(chan struct{})
			}
			mu.Unlock()
		case "/release":
			release()
		case "/deny":
			mu.Lock()
			deny = true
			mu.Unlock()
		case "/allow":
			mu.Lock()
			deny = false
			mu.Unlock()
		case "/status":
			mu.Lock()
			n := blocked
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]int{"blocked": n, "objects": s3.Count()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer func() {
		release()
		control.Close()
		for i := len(own.cleanup) - 1; i >= 0; i-- {
			own.cleanup[i]()
		}
	}()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"endpoint": s3.Server.URL, "control": control.URL})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	eof := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(eof) }()
	select {
	case <-signals:
	case <-eof:
	}
}
