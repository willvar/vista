//go:build linux

package worker

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"vista/internal/platform"
	"vista/internal/resources"
	"vista/internal/workerproto"
)

func TestCancelledResourceRPCProtectsPublicationUntilOwnershipAck(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "vista", "state")
	inputs := filepath.Join(state, "inputs")
	if err := os.MkdirAll(inputs, 0700); err != nil {
		t.Fatal(err)
	}
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lN8AAAAASUVORK5CYII=")
	first := filepath.Join(inputs, "first")
	second := filepath.Join(inputs, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, png, 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(os.Args[0], "-test.run=^TestWorkerAckHelper$")
	command.Env = append(os.Environ(), "VISTA_WORKER_ACK_HELPER=1", "XDG_CONFIG_HOME="+root)
	platform.AttachToParent(command)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr lockedBuffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); command.Process.Kill(); command.Wait() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "READY" {
		t.Fatal("Worker did not become ready")
	}
	events := make(chan workerproto.Result, 32)
	go func() {
		defer close(events)
		for scanner.Scan() {
			var event workerproto.Result
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			}
		}
	}()
	send := func(value workerproto.Command) {
		t.Helper()
		if err := json.NewEncoder(input).Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	next := func() workerproto.Result {
		t.Helper()
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("Worker exited", stderr.String())
			}
			if event.Kind == "metrics" {
				return nextResourceEvent(t, events, &stderr)
			}
			return event
		case <-time.After(3 * time.Second):
			t.Fatal("Worker IPC deadline")
			return workerproto.Result{}
		}
	}
	send(workerproto.Command{Action: "import_resource", RPCID: "first", RunID: "first", SessionID: "fixture", ResourcePath: first, ResourceName: "first.png", ResourceType: "image/png"})
	admission := next()
	if admission.Kind != "media_admission" {
		t.Fatal("missing media admission")
	}
	send(workerproto.Command{Action: "ack", RunID: admission.RunID, AckID: admission.AckID})
	publication := next()
	if publication.Kind != "resource_import" || publication.ResourceStorage == nil {
		t.Fatal("missing private publication stamp")
	}
	send(workerproto.Command{Action: "cancel_rpc", RPCID: "first", RunID: "first"})
	// This later command's admission proves the IPC reader processed cancellation;
	// leave its admission unacknowledged so it cannot acquire the resource lock.
	send(workerproto.Command{Action: "import_resource", RPCID: "after-cancel", RunID: "after-cancel", SessionID: "fixture", ResourcePath: second, ResourceType: "image/png"})
	observed := next()
	if observed.Kind != "media_admission" || observed.RunID != "after-cancel" {
		t.Fatal("cancelled publication released before ownership acknowledgement")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	release, err := (resources.Library{StateDir: state}).Lock(ctx)
	cancel()
	if err == nil {
		release()
		t.Fatal("RPC cancellation released a published original before Server ACK")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	send(workerproto.Command{Action: "ack", RunID: publication.RunID, AckID: publication.AckID})
	result := next()
	if result.Kind != "rpc" || result.RPCID != "first" || result.Status != "failed" {
		t.Fatal("cancelled RPC did not settle after durable ACK")
	}
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	release, err = (resources.Library{StateDir: state}).Lock(ctx)
	if err != nil {
		t.Fatal("resource lock was not released after ACK", err)
	}
	release()
}

func nextResourceEvent(t *testing.T, events <-chan workerproto.Result, stderr *lockedBuffer) workerproto.Result {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("Worker exited", stderr.String())
			}
			if event.Kind == "metrics" {
				continue
			}
			return event
		case <-deadline.C:
			t.Fatal("Worker IPC deadline")
			return workerproto.Result{}
		}
	}
}
