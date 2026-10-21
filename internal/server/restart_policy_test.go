package server

import (
	"testing"
	"time"
)

func TestRepeatedShortLivedWorkerRecoveryIsSuppressed(t *testing.T) {
	a := &App{}
	now := time.Now()
	for attempt := 0; attempt < maxWorkerExitsPerWindow; attempt++ {
		if !a.recordWorkerFailureLocked("session", now.Add(time.Duration(attempt)*time.Second)) {
			t.Fatalf("early exit %d suppressed", attempt)
		}
	}
	if a.recordWorkerFailureLocked("session", now.Add(4*time.Second)) {
		t.Fatal("fourth short-lived Worker was restarted automatically")
	}
	if !a.recordWorkerFailureLocked("other-session", now.Add(5*time.Second)) {
		t.Fatal("one Session's crashes blocked another Session")
	}
	if !a.recordWorkerFailureLocked("session", now.Add(2*restartWindow)) {
		t.Fatal("failure window did not expire")
	}
}
