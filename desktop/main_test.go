package main

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestDesktopInstanceID(t *testing.T) {
	// The previous Linux construction was invalid for a hash beginning with 5.
	root := "0"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(root)))
	if !strings.HasPrefix(desktopInstanceID(root, "linux"), desktopID+".config_") {
		t.Fatal("the Linux bus name has no non-numeric element before the digest")
	}
	if got := desktopInstanceID(root, "windows"); got != desktopID+"."+sum {
		t.Fatalf("pre-existing Windows instance name changed: %q", got)
	}
}
