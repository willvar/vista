package main

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

func TestWindowsLoginEntry(t *testing.T) {
	e := loginRunEntry{fmt.Sprintf(`Software\%s.test-%d`, desktopID, time.Now().UnixNano()), desktopID}
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, e.key) })
	assert := func(want bool) {
		t.Helper()
		if got, err := e.enabled(); err != nil || got != want {
			t.Fatalf("login=%v: %v, want %v", got, err, want)
		}
	}
	assert(false)
	if err := e.set(`C:\Test Path\Vista\vista-desktop.exe`, true); err != nil {
		t.Fatal(err)
	}
	assert(true)
	key, err := registry.OpenKey(registry.CURRENT_USER, e.key, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := key.GetStringValue(e.name)
	key.Close()
	if err != nil || got != `"C:\Test Path\Vista\vista-desktop.exe" --tray` {
		t.Fatalf("unexpected login command: %q: %v", got, err)
	}
	if err := e.set("", false); err != nil {
		t.Fatal(err)
	}
	assert(false)
}
