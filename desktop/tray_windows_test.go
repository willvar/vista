package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"vista/internal/instance"
	"vista/internal/platform"
)

var (
	testUser  = windows.NewLazySystemDLL("user32.dll")
	testShell = windows.NewLazySystemDLL("shell32.dll")
)

//go:uintptrescapes
func user(name string, args ...uintptr) uintptr {
	value, _, _ := testUser.NewProc(name).Call(args...)
	return value
}

func windowsOf(parent uintptr, pid int, class, title string) []uintptr {
	var result []uintptr
	for window := uintptr(0); ; {
		var className, caption *uint16
		if class != "" {
			className, _ = windows.UTF16PtrFromString(class)
		}
		if title != "" {
			caption, _ = windows.UTF16PtrFromString(title)
		}
		window = user("FindWindowExW", parent, window, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(caption)))
		if window == 0 {
			return result
		}
		var owner uint32
		user("GetWindowThreadProcessId", window, uintptr(unsafe.Pointer(&owner)))
		if int(owner) == pid {
			result = append(result, window)
		}
	}
}

func nativeWait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	for until := time.Now().Add(14 * time.Second); time.Now().Before(until); time.Sleep(40 * time.Millisecond) {
		if ready() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", description)
}

type iconIdentifier struct {
	Size   uint32
	Window uintptr
	ID     uint32
	GUID   windows.GUID
}

func trayIcon(window uintptr) bool {
	id := iconIdentifier{Window: window, ID: 1}
	id.Size = uint32(unsafe.Sizeof(id))
	var rect [4]int32
	value, _, _ := testShell.NewProc("Shell_NotifyIconGetRect").Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&rect)))
	return int32(value) == 0
}

func findTray(pid int) []uintptr {
	var result []uintptr
	for _, w := range windowsOf(^uintptr(2), pid, "WailsWebviewWindow", "") {
		if trayIcon(w) {
			result = append(result, w)
		}
	}
	return result
}

func message(window, msg, wp, lp uintptr) uintptr {
	var result uintptr
	user("SendMessageTimeoutW", window, msg, wp, lp, 2, 1500, uintptr(unsafe.Pointer(&result)))
	return result
}

type menuItem struct {
	id      uintptr
	enabled bool
}

func testMenu(t *testing.T, pid int, tray uintptr) map[string]menuItem {
	t.Helper()
	user("PostMessageW", tray, 0x401, 0, 0x7b) // Wails' version-4 tray right-click.
	var menuWindow uintptr
	nativeWait(t, "native tray menu", func() bool {
		for _, popup := range windowsOf(0, pid, "#32768", "") {
			if user("IsWindowVisible", popup) != 0 {
				menuWindow = popup
				return true
			}
		}
		return false
	})
	hmenu := message(menuWindow, 0x01e1, 0, 0) // MN_GETHMENU in the native popup class.
	if hmenu == 0 {
		t.Fatal("no menu handle on the tray popup")
	}
	labels := map[string]menuItem{}
	for i, total := uintptr(0), user("GetMenuItemCount", hmenu); i < total && i < 16; i++ {
		var buf [256]uint16
		user("GetMenuStringW", hmenu, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0x400)
		label := windows.UTF16ToString(buf[:])
		if label != "" {
			labels[label] = menuItem{user("GetMenuItemID", hmenu, i), user("GetMenuState", hmenu, i, 0x400)&3 == 0}
		}
	}
	user("PostMessageW", tray, 0x001f, 0, 0) // Cancel the test menu through its owner.
	nativeWait(t, "tray menu dismissal", func() bool { return user("IsWindowVisible", menuWindow) == 0 })
	for _, name := range []string{"Open Vista", "Status", "Restart", "Stop", "Quit desktop"} {
		if labels[name].id == 0 {
			t.Fatalf("missing %q in tray: %v", name, labels)
		}
	}
	return labels
}

func handleTitle(pid int, title string) uintptr {
	for _, w := range windowsOf(0, pid, "", title) {
		return w
	}
	return 0
}

func clickDialog(t *testing.T, pid int, title, button string) {
	t.Helper()
	var control uintptr
	nativeWait(t, title+" confirmation", func() bool {
		w := handleTitle(pid, title)
		if w == 0 {
			return false
		}
		for _, child := range windowsOf(w, pid, "Button", "") {
			var text [128]uint16
			message(child, 0x000d, uintptr(len(text)), uintptr(unsafe.Pointer(&text[0])))
			if strings.EqualFold(strings.ReplaceAll(windows.UTF16ToString(text[:]), "&", ""), button) || button == "Yes" && user("GetDlgCtrlID", child) == 6 {
				control = child
				return true
			}
		}
		return false
	})
	user("PostMessageW", control, 0x00f5, 0, 0) // BM_CLICK on the verified dialog button.
}

func TestTrayRequiresPairedCompanion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vista.exe")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	const hash = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if !matchesExecutable(path, hash) {
		t.Fatal("the matching companion was rejected")
	}
	for _, invalid := range []string{"", strings.Repeat("0", 64)} {
		if matchesExecutable(path, invalid) {
			t.Fatal("an older or unverifiable service would receive another tray")
		}
	}
	if matchesExecutable(path+".missing", hash) {
		t.Fatal("missing paired executable was accepted")
	}
}

func TestNativeWindowsTray(t *testing.T) {
	binary := os.Getenv("VISTA_TEST_DESKTOP")
	service := binary
	if binary == "" {
		t.Skip("set VISTA_TEST_DESKTOP to the standalone desktop executable")
	}
	taskbar, _ := windows.UTF16PtrFromString("Shell_TrayWnd")
	if user("FindWindowW", uintptr(unsafe.Pointer(taskbar)), 0) == 0 {
		t.Skip("requires Explorer in the interactive Windows session")
	}
	root, err := os.MkdirTemp("", "vista-tray-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The WebView2 subprocesses can finish writing their private profile
		// after the desktop exits. Keep the cleanup bounded and require success.
		var err error
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if err = os.RemoveAll(root); err == nil {
				return
			}
		}
		t.Errorf("remove private desktop profile: %v", err)
	})
	t.Setenv("APPDATA", root)
	t.Setenv("VISTA_HOST", "127.0.0.1")
	t.Setenv("VISTA_NO_BROWSER", "1")
	for _, name := range []string{"VISTA_OPENAI_API_KEY", "VISTA_ANTHROPIC_API_KEY", "VISTA_GEMINI_API_KEY", "VISTA_MISTRAL_API_KEY", "VISTA_DEFAULT_MODEL"} {
		t.Setenv(name, "")
	}
	config := filepath.Join(root, "vista")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "model-catalog-status.json"), []byte(`{"nextCheckAt":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	listen, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listen.Addr().(*net.TCPAddr).Port
	listen.Close()
	t.Setenv("VISTA_PORT", strconv.Itoa(port))
	base, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d/", port))
	output, err := os.Create(filepath.Join(root, "desktop.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd := exec.Command(binary)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		if platform.IsAlive(pid) {
			_ = cmd.Process.Kill()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, service, "stop").Run() // Same isolated APPDATA.
		if t.Failed() {
			if data, e := os.ReadFile(filepath.Join(root, "desktop.log")); e == nil {
				t.Log(string(data))
			}
		}
	})
	var window, tray uintptr
	var before serviceStatus
	nativeWait(t, "desktop window and one tray for its local service", func() bool {
		window = handleTitle(pid, "Vista")
		icons := findTray(pid)
		if len(icons) != 1 || window == 0 || user("IsWindowVisible", window) == 0 {
			return false
		}
		tray = icons[0]
		before, err = readStatus(base)
		return err == nil && before.PID != pid && len(findTray(before.PID)) == 0 && len(windowsOf(0, before.PID, "VistaDesktopControls", "")) == 0
	})
	secondary := exec.Command(binary)
	if err := secondary.Run(); err != nil {
		t.Fatalf("second desktop activation: %v", err)
	}
	if len(findTray(pid)) != 1 {
		t.Fatal("second activation created another tray")
	}
	user("PostMessageW", window, 0x10, 0, 0)
	nativeWait(t, "close hides the workspace but preserves the tray", func() bool { return user("IsWindowVisible", window) == 0 && trayIcon(tray) })
	user("PostMessageW", tray, 0x401, 0, 0x202)
	nativeWait(t, "tray reopens the same workspace", func() bool { return user("IsWindowVisible", window) != 0 })
	menu := testMenu(t, pid, tray)
	user("PostMessageW", tray, 0x111, menu["Status"].id, 0)
	var status uintptr
	nativeWait(t, "native service status", func() bool { status = handleTitle(pid, "Vista Status"); return status != 0 })
	user("PostMessageW", status, 0x10, 0, 0)
	nativeWait(t, "status dismissal", func() bool { return user("IsWindow", status) == 0 })
	user("PostMessageW", tray, 0x111, menu["Restart"].id, 0)
	clickDialog(t, pid, "Restart Vista", "Yes")
	var after serviceStatus
	dir := filepath.Join(config, "state")
	nativeWait(t, "restart replaces only the companion", func() bool {
		after, err = readStatus(base)
		return err == nil && after.PID != before.PID && after.InstanceID != before.InstanceID && registered(dir, base, after.PID) && trayIcon(tray)
	})
	// Coming online does not imply the tray has finished its restart check.
	nativeWait(t, "tray controls re-enabled after restart", func() bool {
		menu = testMenu(t, pid, tray)
		return menu["Stop"].enabled
	})
	user("PostMessageW", tray, 0x111, menu["Stop"].id, 0)
	clickDialog(t, pid, "Stop Vista", "Yes")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(17 * time.Second):
		t.Fatal("desktop did not quit after Stop")
	}
	if instance.IsAlive(after.PID) || trayIcon(tray) {
		t.Fatal("Stop left the companion or tray running")
	}
	t.Run("unmatched running service", func(t *testing.T) {
		// Windows accepts appended bytes without changing the PE entry point.
		// A different binary hash models an already-running older companion
		// without packaging legacy tray code in the new test or product.
		old := filepath.Join(root, "other-vista.exe")
		bytes, err := os.ReadFile(service)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(old, append(bytes, 0), 0700); err != nil {
			t.Fatal(err)
		}
		oldServer := exec.Command(old, "serve")
		oldServer.Stdout, oldServer.Stderr = output, output
		if err := oldServer.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			_ = exec.CommandContext(ctx, old, "stop").Run()
			if platform.IsAlive(oldServer.Process.Pid) {
				_ = oldServer.Process.Kill()
			}
			_ = oldServer.Wait()
		})
		nativeWait(t, "isolated different companion", func() bool {
			s, e := readStatus(base)
			return e == nil && s.PID == oldServer.Process.Pid && s.BinaryHash != "" && s.BinaryHash != after.BinaryHash
		})
		viewer := exec.Command(binary)
		viewer.Stdout, viewer.Stderr = output, output
		if err := viewer.Start(); err != nil {
			t.Fatal(err)
		}
		viewExit := make(chan error, 1)
		go func() { viewExit <- viewer.Wait() }()
		t.Cleanup(func() {
			if platform.IsAlive(viewer.Process.Pid) {
				_ = viewer.Process.Kill()
			}
		})
		var view uintptr
		nativeWait(t, "WebUI connects to the unmatched service", func() bool {
			view = handleTitle(viewer.Process.Pid, "Vista")
			s, e := readStatus(base)
			return view != 0 && e == nil && s.EventStreams >= 1 && s.PID == oldServer.Process.Pid
		})
		if len(findTray(viewer.Process.Pid)) != 0 {
			t.Fatal("created a second tray for an unmatched service")
		}
		user("PostMessageW", view, 0x10, 0, 0)
		select {
		case e := <-viewExit:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a window without its own tray did not close")
		}
		if s, e := readStatus(base); e != nil || s.PID != oldServer.Process.Pid {
			t.Fatal("viewing an unmatched service interrupted it")
		}
	})
}
