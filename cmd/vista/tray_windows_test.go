package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"vista/internal/instance"
	"vista/internal/platform"
)

func findNativeWindow(pid int, class, title string) uintptr {
	var after uintptr
	for {
		hwnd := userCall("FindWindowExW", 0, after, uintptr(unsafe.Pointer(nativeText(class))), uintptr(unsafe.Pointer(nativeText(title))))
		if hwnd == 0 {
			return 0
		}
		var owner uint32
		userCall("GetWindowThreadProcessId", hwnd, uintptr(unsafe.Pointer(&owner)))
		if int(owner) == pid {
			return hwnd
		}
		after = hwnd
	}
}

func trayTestWait(t *testing.T, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", description)
}

func nativeControlText(hwnd uintptr) string {
	var length uintptr
	proc := desktopUser.NewProc("SendMessageTimeoutW")
	if result, _, _ := proc.Call(hwnd, 0x000e, 0, 0, 2, 500, uintptr(unsafe.Pointer(&length))); result == 0 {
		return ""
	}
	buffer := make([]uint16, min(length, 10<<20)+1)
	var copied uintptr
	_, _, _ = proc.Call(hwnd, 0x000d, uintptr(len(buffer)), uintptr(unsafe.Pointer(&buffer[0])), 2, 500, uintptr(unsafe.Pointer(&copied)))
	return windows.UTF16ToString(buffer)
}

func trayIconRect(hwnd uintptr) bool {
	identifier := struct {
		Size   uint32
		Window uintptr
		ID     uint32
		GUID   windows.GUID
	}{Window: hwnd, ID: 1}
	identifier.Size = uint32(unsafe.Sizeof(identifier))
	var rect nativeRect
	result, _, _ := desktopShell.NewProc("Shell_NotifyIconGetRect").Call(uintptr(unsafe.Pointer(&identifier)), uintptr(unsafe.Pointer(&rect)))
	return int32(result) == 0
}

func copyTrayBrowserHelper(t *testing.T, destination string) {
	t.Helper()
	source, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, source)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy browser helper: %v %v", copyErr, closeErr)
	}
}

func TestWindowsTrayMenu(t *testing.T) {
	menu, err := newTrayMenu()
	if err != nil {
		t.Fatal(err)
	}
	defer userCall("DestroyMenu", menu)
	if count := userCall("GetMenuItemCount", menu); count != 4 {
		t.Fatalf("menu has %d items", count)
	}
	for index, want := range map[uintptr]string{0: "Restart", 1: "Status", 3: "Stop"} {
		var text [64]uint16
		userCall("GetMenuStringW", menu, index, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)), 0x400)
		if got := windows.UTF16ToString(text[:]); got != want {
			t.Fatalf("menu item %d: %q", index, got)
		}
	}
	if userCall("GetMenuState", menu, 2, 0x400)&0x800 == 0 {
		t.Fatal("missing separator before Stop")
	}
	if unsafe.Sizeof(uintptr(0)) == 8 && (unsafe.Sizeof(nativeNotifyIcon{}) != 976 || unsafe.Sizeof(nativeMessage{}) != 48 || unsafe.Sizeof(nativeWindowClass{}) != 80) {
		t.Fatal("Win32 struct layout mismatch")
	}
}

func TestNativeWindowsTray(t *testing.T) {
	binary := os.Getenv("VISTA_TEST_BINARY")
	if binary == "" {
		t.Skip("set VISTA_TEST_BINARY to the application executable")
	}
	if userCall("FindWindowW", uintptr(unsafe.Pointer(nativeText("Shell_TrayWnd"))), 0) == 0 {
		t.Skip("run in an interactive Windows session with Explorer")
	}
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	for _, key := range []string{"VISTA_OPENAI_API_KEY", "VISTA_ANTHROPIC_API_KEY", "VISTA_GEMINI_API_KEY", "VISTA_MISTRAL_API_KEY", "VISTA_DEFAULT_MODEL"} {
		t.Setenv(key, "")
	}
	config := filepath.Join(root, "vista")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "model-catalog-status.json"), []byte(`{"nextCheckAt":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	t.Setenv("VISTA_PORT", strconv.Itoa(port))
	t.Setenv("VISTA_NO_BROWSER", "0")
	browserLog := filepath.Join(root, "browser.jsonl")
	t.Setenv("VISTA_LAUNCH_BROWSER_LOG", browserLog)
	helpers := filepath.Join(root, "browser")
	if err := os.MkdirAll(helpers, 0700); err != nil {
		t.Fatal(err)
	}
	copyTrayBrowserHelper(t, filepath.Join(helpers, "rundll32.exe"))
	t.Setenv("PATH", helpers+";"+os.Getenv("PATH"))
	cli := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		return string(output), err
	}
	t.Cleanup(func() {
		if output, err := cli("stop"); err != nil {
			t.Errorf("stop isolated service: %v %s", err, output)
		}
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(config, "state", "server.log"))
			t.Logf("Server log:\n%s", data)
		}
	})
	if output, err := cli("start"); err != nil {
		t.Fatalf("start: %v %s", err, output)
	}
	d := filepath.Join(config, "state")
	before, err := instance.Read(d)
	if err != nil {
		t.Fatal(err)
	}
	var tray uintptr
	trayTestWait(t, "tray window and icon", func() bool {
		tray = findNativeWindow(before.PID, "VistaDesktopControls", "Vista Tray")
		return tray != 0 && trayIconRect(tray)
	})
	if output, err := cli("start"); err != nil {
		t.Fatalf("reopen: %v %s", err, output)
	}
	if after, _ := instance.Read(d); after.PID != before.PID {
		t.Fatal("reopening created another Server")
	}
	if next := userCall("FindWindowExW", 0, tray, uintptr(unsafe.Pointer(nativeText("VistaDesktopControls"))), uintptr(unsafe.Pointer(nativeText("Vista Tray")))); next != 0 {
		var pid uint32
		userCall("GetWindowThreadProcessId", next, uintptr(unsafe.Pointer(&pid)))
		if int(pid) == before.PID {
			t.Fatal("duplicate tray window")
		}
	}
	countBrowser := func() int {
		data, _ := os.ReadFile(browserLog)
		if len(data) == 0 {
			return 0
		}
		return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
	}
	trayTestWait(t, "two initial browser opens", func() bool { return countBrowser() == 2 })
	activate := func(event uintptr) { userCall("PostMessageW", tray, wmTray, 0, (1<<16)|event) }
	activate(ninSelect)
	trayTestWait(t, "single click Start", func() bool { return countBrowser() == 3 })
	activate(ninSelect)
	activate(wmLButtonDouble)
	activate(ninSelect)
	trayTestWait(t, "double click Start", func() bool { return countBrowser() == 4 })
	time.Sleep(time.Duration(userCall("GetDoubleClickTime"))*time.Millisecond + 150*time.Millisecond)
	if countBrowser() != 4 {
		t.Fatalf("double click opened multiple browsers: %d", countBrowser())
	}
	activate(wmContextMenu)
	trayTestWait(t, "right-click popup", func() bool { return findNativeWindow(before.PID, "#32768", "") != 0 })
	userCall("PostMessageW", tray, 0x001f, 0, 0) // WM_CANCELMODE dismisses the popup without user input.
	trayTestWait(t, "dismissed popup", func() bool { return findNativeWindow(before.PID, "#32768", "") == 0 })
	// A real Worker puts the Server in its supervision Job. Restart must work
	// in that state and still preserve the conversation.
	response, err := http.Post(before.Address+"/api/sessions", "application/json", strings.NewReader(`{"title":"Tray lifecycle check"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 201 {
		t.Fatalf("create conversation: %d %s", response.StatusCode, data)
	}
	var session struct{ ID string }
	_ = json.Unmarshal(data, &session)
	userCall("PostMessageW", tray, wmCommand, trayStatus, 0)
	var statusWindow, edit uintptr
	trayTestWait(t, "parsed native status", func() bool {
		statusWindow = findNativeWindow(before.PID, "VistaDesktopControls", "Vista Status")
		if statusWindow == 0 {
			return false
		}
		edit = userCall("FindWindowExW", statusWindow, 0, uintptr(unsafe.Pointer(nativeText("EDIT"))), 0)
		text := nativeControlText(edit)
		return strings.Contains(text, "VISTA STATUS") && strings.Contains(text, "Workers: 1") && strings.Contains(text, session.ID) && strings.Contains(text, "No model connected.") && strings.Contains(text, before.Address)
	})
	userCall("PostMessageW", tray, wmCommand, trayStatus, 0)
	time.Sleep(100 * time.Millisecond)
	if got := findNativeWindow(before.PID, "VistaDesktopControls", "Vista Status"); got != statusWindow {
		t.Fatal("Status created a duplicate window")
	}
	userCall("PostMessageW", statusWindow, wmClose, 0, 0)
	trayTestWait(t, "status closed independently", func() bool { return userCall("IsWindow", statusWindow) == 0 && trayIconRect(tray) })
	// Simulate only the shell's loss/recreation of this isolated icon. Do not
	// restart the user's Explorer process.
	icon := nativeNotifyIcon{Window: tray, ID: 1}
	icon.Size = uint32(unsafe.Sizeof(icon))
	_, _, _ = desktopShell.NewProc("Shell_NotifyIconW").Call(2, uintptr(unsafe.Pointer(&icon)))
	if trayIconRect(tray) {
		t.Fatal("test icon was not removed")
	}
	created := userCall("RegisterWindowMessageW", uintptr(unsafe.Pointer(nativeText("TaskbarCreated"))))
	userCall("PostMessageW", tray, created, 0, 0)
	trayTestWait(t, "icon restored after TaskbarCreated", func() bool { return trayIconRect(tray) })
	userCall("PostMessageW", tray, wmCommand, trayRestart, 0)
	var after instance.Info
	trayTestWait(t, "Restart starts a new Server and tray", func() bool {
		after, _ = instance.Read(d)
		if after.PID == 0 || after.PID == before.PID {
			return false
		}
		next := findNativeWindow(after.PID, "VistaDesktopControls", "Vista Tray")
		if next == 0 || !trayIconRect(next) {
			return false
		}
		tray = next
		return true
	})
	if platform.IsAlive(before.PID) {
		t.Fatal("old Server survived Restart")
	}
	response, err = http.Get(after.Address + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(data), session.ID) {
		t.Fatal("Restart lost the conversation")
	}
	// Stop must also close an open status window and remove the icon.
	userCall("PostMessageW", tray, wmCommand, trayStatus, 0)
	trayTestWait(t, "status reopened after Restart", func() bool { return findNativeWindow(after.PID, "VistaDesktopControls", "Vista Status") != 0 })
	userCall("PostMessageW", tray, wmCommand, trayStop, 0)
	trayTestWait(t, "Stop releases service and tray", func() bool {
		_, running := probe(d)
		return !running && !platform.IsAlive(after.PID) && userCall("IsWindow", tray) == 0 && !trayIconRect(tray)
	})
	lock, err := instance.Lock(d)
	if err != nil {
		t.Fatalf("Stop retained instance lock: %v", err)
	}
	_ = instance.Unlock(lock)
}

func TestNativeWindowsTrayLongStatus(t *testing.T) {
	if os.Getenv("VISTA_TEST_BINARY") == "" {
		t.Skip("enable native UI checks with VISTA_TEST_BINARY")
	}
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	d, err := dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d, 0700); err != nil {
		t.Fatal(err)
	}
	workers := make([]map[string]any, 300)
	for i := range workers {
		workers[i] = map[string]any{"sessionId": fmt.Sprintf("conversation-%03d", i), "pid": 1000 + i, "memory": map[string]any{"sampledAt": "2026-10-22T03:04:05Z", "rssBytes": 1048576}}
	}
	var slow atomic.Bool
	var slowCalls atomic.Int32
	requestStarted := make(chan struct{}, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() && slowCalls.Add(1) > 1 {
			requestStarted <- struct{}{}
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "running", "model": "长模型名称 · Unicode", "workerMetrics": workers})
	}))
	defer endpoint.Close()
	if err := instance.Write(d, instance.Info{PID: os.Getpid(), Address: endpoint.URL}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanup := startDesktopControls(ctx)
	if cleanup == nil {
		t.Fatal("tray failed to initialize")
	}
	defer cleanup()
	tray := findNativeWindow(os.Getpid(), "VistaDesktopControls", "Vista Tray")
	if tray == 0 {
		t.Fatal("missing tray window")
	}
	userCall("PostMessageW", tray, wmCommand, trayStatus, 0)
	var status, edit uintptr
	var lastText string
	defer func() {
		if t.Failed() {
			t.Logf("Last native status (%d bytes): %s", len(lastText), lastText[max(0, len(lastText)-500):])
		}
	}()
	trayTestWait(t, "complete long Unicode status", func() bool {
		status = findNativeWindow(os.Getpid(), "VistaDesktopControls", "Vista Status")
		if status == 0 {
			return false
		}
		edit = userCall("FindWindowExW", status, 0, uintptr(unsafe.Pointer(nativeText("EDIT"))), 0)
		lastText = nativeControlText(edit)
		return len(lastText) > 65536 && strings.Contains(lastText, "conversation-299") && strings.Contains(lastText, "长模型名称 · Unicode")
	})
	if style := userCall("GetWindowLongW", edit, uintptr(uint32(0xfffffff0))); style&0x00200000 == 0 || style&0x0800 == 0 {
		t.Fatal("status text is not scrollable and read-only")
	}
	userCall("SendMessageW", edit, 0x00b6, 0, 10000) // EM_LINESCROLL
	if line := userCall("SendMessageW", edit, 0x00ce, 0, 0); line == 0 {
		t.Fatal("long status did not scroll")
	}
	// Refresh into a slow HTTP request. Native controls and service shutdown
	// must stay responsive even before the status deadline expires.
	slow.Store(true)
	userCall("PostMessageW", tray, wmCommand, trayStatus, 0)
	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("slow status request did not start")
	}
	started := time.Now()
	cancel()
	trayTestWait(t, "status closed on service cancellation", func() bool { return userCall("IsWindow", status) == 0 })
	if time.Since(started) > 2*time.Second {
		t.Fatal("pending status request blocked shutdown")
	}
}
