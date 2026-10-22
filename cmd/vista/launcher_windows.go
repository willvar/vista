package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func prepareLauncher() bool {
	control := os.Getenv("VISTA_DESKTOP_CONTROL") == "1"
	// This marker belongs only to a hidden tray action, never its Server or
	// Workers. It lets the independent action report failures after shutdown.
	_ = os.Unsetenv("VISTA_DESKTOP_CONTROL")
	if !control && !startedFromExplorer() {
		return false
	}
	// Only hide this launcher's own console. A terminal invocation keeps its
	// console and normal redirected input/output; the Server detaches itself.
	window, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	if window != 0 {
		_, _, _ = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow").Call(window, windows.SW_HIDE)
	}
	return true
}

func startedFromExplorer() bool {
	parent, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getppid()))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(parent)
	var path [32768]uint16
	size := uint32(len(path))
	if err := windows.QueryFullProcessImageName(parent, 0, &path[0], &size); err != nil {
		return false
	}
	return strings.EqualFold(filepath.Base(windows.UTF16ToString(path[:size])), "explorer.exe")
}

func showLauncherError(message string) {
	text, _ := windows.UTF16PtrFromString(message)
	title, _ := windows.UTF16PtrFromString("Vista")
	_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR|windows.MB_SETFOREGROUND)
}
