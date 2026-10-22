package main

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"vista/internal/platform"
)

//go:embed assets/vista.ico
var desktopIcon []byte

const (
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmTimer         = 0x0113
	wmContextMenu   = 0x007b
	wmLButtonUp     = 0x0202
	wmLButtonDouble = 0x0203
	wmRButtonUp     = 0x0205
	wmDPIChanged    = 0x02e0
	wmTray          = 0x8001
	wmDesktopResult = 0x8002
	ninSelect       = 0x0400
	ninKeySelect    = 0x0401
	trayClickTimer  = 1
	trayRestart     = 101
	trayStatus      = 102
	trayStop        = 103
	statusClose     = 2 // IDCANCEL also handles Escape through IsDialogMessage.
)

var (
	desktopUser       = windows.NewLazySystemDLL("user32.dll")
	desktopShell      = windows.NewLazySystemDLL("shell32.dll")
	desktopGDI        = windows.NewLazySystemDLL("gdi32.dll")
	desktopKernel     = windows.NewLazySystemDLL("kernel32.dll")
	desktopClass      sync.Once
	desktopClassErr   error
	desktopWindows    sync.Map // HWND -> nativeWindowHandler; each runs on its UI thread.
	desktopWindowProc = windows.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
		if handler, ok := desktopWindows.Load(hwnd); ok {
			return handler.(nativeWindowHandler)(hwnd, msg, wp, lp)
		}
		return userCall("DefWindowProcW", hwnd, uintptr(msg), wp, lp)
	})
)

type nativeWindowHandler func(uintptr, uint32, uintptr, uintptr) uintptr
type nativePoint struct{ X, Y int32 }
type nativeRect struct{ Left, Top, Right, Bottom int32 }
type nativeMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          nativePoint
	Private        uint32
}
type nativeWindowClass struct {
	Size, Style                        uint32
	WindowProc                         uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	SmallIcon                          uintptr
}
type nativeNotifyIcon struct {
	Size                       uint32
	Window                     uintptr
	ID, Flags, CallbackMessage uint32
	Icon                       uintptr
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	Version                    uint32
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GUID                       windows.GUID
	BalloonIcon                uintptr
}
type desktopResult struct {
	statusGeneration uint64
	text             string
	actionDone       bool
}
type windowsTray struct {
	ctx                                                context.Context
	done                                               chan struct{}
	results                                            chan desktopResult
	window, icon, menu, taskbarCreated                 uintptr
	statusWindow, statusEdit, statusButton, statusFont uintptr
	statusGeneration                                   uint64
	statusCancel                                       context.CancelFunc
	actionBusy                                         bool
}

//go:uintptrescapes
func userCall(name string, args ...uintptr) uintptr {
	result, _, _ := desktopUser.NewProc(name).Call(args...)
	return result
}

func nativeText(value string) *uint16 {
	// Provider/model names are display data and may contain arbitrary text.
	return windows.StringToUTF16Ptr(strings.ReplaceAll(value, "\x00", "\ufffd"))
}

func registerDesktopClass() error {
	desktopClass.Do(func() {
		module, _, _ := desktopKernel.NewProc("GetModuleHandleW").Call(0)
		class := nativeWindowClass{WindowProc: desktopWindowProc, Instance: module,
			Cursor: userCall("LoadCursorW", 0, 32512), Background: 16,
			ClassName: nativeText("VistaDesktopControls")}
		class.Size = uint32(unsafe.Sizeof(class))
		if result, _, err := desktopUser.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&class))); result == 0 {
			desktopClassErr = fmt.Errorf("register desktop window: %w", err)
		}
	})
	return desktopClassErr
}

func startDesktopControls(ctx context.Context) func() {
	tray := &windowsTray{ctx: ctx, done: make(chan struct{}), results: make(chan desktopResult, 8)}
	type startup struct {
		window uintptr
		err    error
	}
	ready := make(chan startup, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(tray.done)
		// DPI awareness is scoped to this UI thread, not the Server or Workers.
		previous := userCall("SetThreadDpiAwarenessContext", ^uintptr(3)) // PER_MONITOR_AWARE_V2
		if previous != 0 {
			defer userCall("SetThreadDpiAwarenessContext", previous)
		}
		if err := tray.init(); err != nil {
			tray.destroy()
			ready <- startup{err: err}
			return
		}
		defer tray.destroy()
		ready <- startup{window: tray.window}
		var message nativeMessage
		for {
			result, _, err := desktopUser.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
			if int32(result) == -1 {
				log.Printf("Windows tray message loop: %v", err)
				return
			}
			if result == 0 {
				return
			}
			if tray.statusWindow != 0 && userCall("IsDialogMessageW", tray.statusWindow, uintptr(unsafe.Pointer(&message))) != 0 {
				continue
			}
			userCall("TranslateMessage", uintptr(unsafe.Pointer(&message)))
			userCall("DispatchMessageW", uintptr(unsafe.Pointer(&message)))
		}
	}()
	started := <-ready
	if started.err != nil {
		log.Printf("Windows tray unavailable: %v", started.err)
		return nil
	}
	go func() {
		select {
		case <-ctx.Done():
			userCall("PostMessageW", started.window, wmClose, 0, 0)
		case <-tray.done:
		}
	}()
	return func() {
		userCall("PostMessageW", started.window, wmClose, 0, 0)
		<-tray.done
	}
}

func (t *windowsTray) init() error {
	if err := registerDesktopClass(); err != nil {
		return err
	}
	var err error
	t.window, _, err = desktopUser.NewProc("CreateWindowExW").Call(0,
		uintptr(unsafe.Pointer(nativeText("VistaDesktopControls"))), uintptr(unsafe.Pointer(nativeText("Vista Tray"))),
		0, 0, 0, 0, 0, 0, 0, 0, 0)
	if t.window == 0 {
		return fmt.Errorf("create tray window: %w", err)
	}
	desktopWindows.Store(t.window, nativeWindowHandler(t.handle))
	t.taskbarCreated = userCall("RegisterWindowMessageW", uintptr(unsafe.Pointer(nativeText("TaskbarCreated"))))
	t.icon, err = loadDesktopIcon()
	if err != nil {
		return err
	}
	t.menu, err = newTrayMenu()
	if err != nil {
		return err
	}
	t.addIcon()
	return nil
}

func newTrayMenu() (uintptr, error) {
	menu, _, err := desktopUser.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		return 0, fmt.Errorf("create tray menu: %w", err)
	}
	for _, item := range []struct {
		id    uintptr
		label string
	}{{trayRestart, "Restart"}, {trayStatus, "Status"}, {0, ""}, {trayStop, "Stop"}} {
		flags := uintptr(0)
		if item.id == 0 {
			flags = 0x800
		} // MF_SEPARATOR
		if result, _, err := desktopUser.NewProc("AppendMenuW").Call(menu, flags, item.id, uintptr(unsafe.Pointer(nativeText(item.label)))); result == 0 {
			userCall("DestroyMenu", menu)
			return 0, fmt.Errorf("add tray menu item: %w", err)
		}
	}
	return menu, nil
}

func (t *windowsTray) notifyData() nativeNotifyIcon {
	data := nativeNotifyIcon{Window: t.window, ID: 1, Flags: 0x1 | 0x2 | 0x4 | 0x80, CallbackMessage: wmTray, Icon: t.icon, Version: 4}
	data.Size = uint32(unsafe.Sizeof(data))
	copy(data.Tip[:], windows.StringToUTF16("Vista"))
	return data
}

func (t *windowsTray) addIcon() {
	data := t.notifyData()
	if result, _, err := desktopShell.NewProc("Shell_NotifyIconW").Call(0, uintptr(unsafe.Pointer(&data))); result == 0 { // NIM_ADD
		// Windows services/SSH sessions may have no Explorer. Keep the hidden
		// top-level window so TaskbarCreated can install the icon if one appears.
		log.Printf("Windows taskbar unavailable; tray will retry when Explorer starts: %v", err)
		return
	}
	if result, _, err := desktopShell.NewProc("Shell_NotifyIconW").Call(4, uintptr(unsafe.Pointer(&data))); result == 0 { // NIM_SETVERSION
		log.Printf("Windows tray notification version: %v", err)
	}
}

func (t *windowsTray) handle(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	if t.taskbarCreated != 0 && uintptr(msg) == t.taskbarCreated {
		t.addIcon()
		return 0
	}
	switch msg {
	case wmTray:
		switch uint16(lp) { // NOTIFYICON_VERSION_4 packs the event in LOWORD.
		case ninSelect, wmLButtonUp, wmLButtonDouble:
			// Deferring a single click by the OS double-click interval folds
			// both activation paths into one Start/browser action.
			userCall("SetTimer", hwnd, trayClickTimer, userCall("GetDoubleClickTime"), 0)
		case ninKeySelect:
			userCall("KillTimer", hwnd, trayClickTimer)
			t.runAction("start")
		case wmContextMenu, wmRButtonUp:
			userCall("KillTimer", hwnd, trayClickTimer)
			t.showMenu()
		}
		return 0
	case wmTimer:
		if wp == trayClickTimer {
			userCall("KillTimer", hwnd, trayClickTimer)
			t.runAction("start")
		}
		return 0
	case wmCommand:
		t.selectMenu(uint16(wp))
		return 0
	case wmDesktopResult:
		for {
			select {
			case result := <-t.results:
				if result.actionDone {
					t.actionBusy = false
				}
				if result.statusGeneration == t.statusGeneration && t.statusWindow != 0 && result.text != "" {
					userCall("SetWindowTextW", t.statusEdit, uintptr(unsafe.Pointer(nativeText(result.text))))
				}
			default:
				return 0
			}
		}
	case wmClose:
		userCall("DestroyWindow", hwnd)
		return 0
	case wmDestroy:
		userCall("PostQuitMessage", 0)
		return 0
	}
	return userCall("DefWindowProcW", hwnd, uintptr(msg), wp, lp)
}

func (t *windowsTray) showMenu() {
	var point nativePoint
	userCall("GetCursorPos", uintptr(unsafe.Pointer(&point)))
	flag := uintptr(0)
	if t.actionBusy {
		flag = 1
	} // MF_GRAYED
	userCall("EnableMenuItem", t.menu, trayRestart, flag)
	userCall("EnableMenuItem", t.menu, trayStop, flag)
	userCall("SetForegroundWindow", t.window)
	selected := userCall("TrackPopupMenu", t.menu, 0x100|0x2, uintptr(point.X), uintptr(point.Y), 0, t.window, 0) // TPM_RETURNCMD | TPM_RIGHTBUTTON
	userCall("PostMessageW", t.window, 0, 0, 0)
	t.selectMenu(uint16(selected))
}

func (t *windowsTray) selectMenu(id uint16) {
	switch id {
	case trayRestart:
		t.runAction("restart")
	case trayStatus:
		t.showStatus()
	case trayStop:
		t.runAction("stop")
	}
}

func (t *windowsTray) sendResult(result desktopResult) {
	select {
	case t.results <- result:
		userCall("PostMessageW", t.window, wmDesktopResult, 0, 0)
	case <-t.done:
	}
}

func (t *windowsTray) runAction(action string) {
	if t.actionBusy || t.ctx.Err() != nil {
		return
	}
	t.actionBusy = true
	go func() {
		// This process must outlive the old Server during Restart/Stop. Never
		// launch it as a managed Worker or execute stop() on the tray thread.
		err := launchDesktopAction(action)
		if err != nil {
			log.Printf("Windows tray %s: %v", action, err)
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				// A child that could not even launch cannot show its own dialog.
				// Keep this off the tray thread so shutdown remains responsive.
				showLauncherError("Vista could not " + action + ".\n\n" + err.Error())
			}
		}
		t.sendResult(desktopResult{actionDone: true})
	}()
}

func launchDesktopAction(action string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	d, err := dir()
	if err != nil {
		return err
	}
	output, err := os.OpenFile(filepath.Join(d, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	cmd := exec.Command(exe, action)
	cmd.Env = append(os.Environ(), "VISTA_DESKTOP_CONTROL=1")
	cmd.Stdout, cmd.Stderr = output, output
	platform.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// The child owns any native failure dialog; waiting here only debounces
	// repeated actions. Exiting the old Server does not terminate the child.
	return cmd.Wait()
}

func (t *windowsTray) destroy() {
	if t.statusWindow != 0 {
		userCall("DestroyWindow", t.statusWindow)
	}
	if t.window != 0 {
		userCall("KillTimer", t.window, trayClickTimer)
		data := t.notifyData()
		_, _, _ = desktopShell.NewProc("Shell_NotifyIconW").Call(2, uintptr(unsafe.Pointer(&data))) // NIM_DELETE
		if userCall("IsWindow", t.window) != 0 {
			userCall("DestroyWindow", t.window)
		}
		desktopWindows.Delete(t.window)
	}
	if t.menu != 0 {
		userCall("DestroyMenu", t.menu)
	}
	if t.icon != 0 {
		userCall("DestroyIcon", t.icon)
	}
}

func loadDesktopIcon() (uintptr, error) {
	// An ICO is a directory followed by individual icon resources; Win32
	// CreateIconFromResourceEx consumes the selected resource, not the ICO.
	data := desktopIcon
	if len(data) < 6 || binary.LittleEndian.Uint16(data[2:]) != 1 {
		return 0, fmt.Errorf("invalid Vista icon")
	}
	count := int(binary.LittleEndian.Uint16(data[4:]))
	if count == 0 || len(data) < 6+count*16 {
		return 0, fmt.Errorf("invalid Vista icon directory")
	}
	size := int(userCall("GetSystemMetrics", 49)) // SM_CXSMICON
	best, distance := 0, 1<<30
	for i := 0; i < count; i++ {
		width := int(data[6+i*16])
		if width == 0 {
			width = 256
		}
		if delta := abs(width - size); delta < distance {
			best, distance = i, delta
		}
	}
	entry := data[6+best*16:]
	length, offset := uint64(binary.LittleEndian.Uint32(entry[8:])), uint64(binary.LittleEndian.Uint32(entry[12:]))
	if length == 0 || offset+length > uint64(len(data)) {
		return 0, fmt.Errorf("invalid Vista icon resource")
	}
	icon, _, err := desktopUser.NewProc("CreateIconFromResourceEx").Call(uintptr(unsafe.Pointer(&data[offset])), uintptr(length), 1, 0x30000, uintptr(size), uintptr(size), 0)
	if icon == 0 {
		return 0, fmt.Errorf("load Vista icon: %w", err)
	}
	return icon, nil
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func (t *windowsTray) showStatus() {
	if t.statusWindow == 0 {
		if err := t.createStatusWindow(); err != nil {
			log.Printf("Windows status window: %v", err)
			return
		}
	}
	userCall("ShowWindow", t.statusWindow, 9) // SW_RESTORE also handles minimization.
	userCall("SetForegroundWindow", t.statusWindow)
	if t.statusCancel != nil {
		t.statusCancel()
	}
	t.statusGeneration++
	generation := t.statusGeneration
	ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
	t.statusCancel = cancel
	go func() {
		defer cancel()
		text, err := loadDesktopStatus(ctx)
		if err != nil {
			text = "Could not read Vista status.\r\n\r\n" + strings.ReplaceAll(err.Error(), "\n", "\r\n")
		}
		t.sendResult(desktopResult{statusGeneration: generation, text: text})
	}()
}
