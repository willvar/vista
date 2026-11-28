package main

import (
	"os"
	"strings"
	"testing"
)

func TestLinuxLoginSetting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	exe := "/opt/Vista Desktop/vista-desktop"
	assert := func(want bool) {
		t.Helper()
		if got, err := loginEnabled(exe); err != nil || got != want {
			t.Fatalf("enabled=%v, error=%v, want %v", got, err, want)
		}
	}
	assert(false)
	if err := setLogin(exe, true); err != nil {
		t.Fatal(err)
	}
	assert(true)
	f, _ := linuxLoginFile()
	raw, err := os.ReadFile(f.path)
	if err != nil || !strings.Contains(string(raw), `Exec="/opt/Vista Desktop/vista-desktop" --tray`+"\n") {
		t.Fatalf("login item=%q, error=%v", raw, err)
	}
	if err := setLogin(exe, false); err != nil {
		t.Fatal(err)
	}
	assert(false)
}

func TestDesktopExecEscapesFieldCodes(t *testing.T) {
	got, err := desktopExec(`/opt/A %f $PATH "x"/vista`, true)
	want := `"/opt/A %%f \\$PATH \\"x\\"/vista" --tray`
	if err != nil || got != want {
		t.Fatalf("got %q, err %v, want %q", got, err, want)
	}
	if _, err := desktopExec("/opt/path\nExec=unexpected", true); err == nil {
		t.Fatal("accepted a multi-line path")
	}
}
