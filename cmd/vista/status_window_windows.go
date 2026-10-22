package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func (t *windowsTray) createStatusWindow() error {
	dpi := userCall("GetDpiForSystem")
	if dpi == 0 {
		dpi = 96
	}
	scale := func(value int) uintptr { return uintptr(value) * dpi / 96 }
	var workArea nativeRect
	userCall("SystemParametersInfoW", 0x30, 0, uintptr(unsafe.Pointer(&workArea)), 0) // SPI_GETWORKAREA
	width, height := scale(660), scale(700)
	if workArea.Right > workArea.Left && workArea.Bottom > workArea.Top {
		width = min(width, uintptr(workArea.Right-workArea.Left)-scale(32))
		height = min(height, uintptr(workArea.Bottom-workArea.Top)-scale(32))
	}
	hwnd, _, err := desktopUser.NewProc("CreateWindowExW").Call(0,
		uintptr(unsafe.Pointer(nativeText("VistaDesktopControls"))), uintptr(unsafe.Pointer(nativeText("Vista Status"))),
		0x00cf0000, 0x80000000, 0x80000000, width, height, t.window, 0, 0, 0) // WS_OVERLAPPEDWINDOW
	if hwnd == 0 {
		return fmt.Errorf("create status window: %w", err)
	}
	t.statusWindow = hwnd
	desktopWindows.Store(hwnd, nativeWindowHandler(t.handleStatus))
	userCall("SendMessageW", hwnd, 0x0080, 0, t.icon) // WM_SETICON, ICON_SMALL
	userCall("SendMessageW", hwnd, 0x0080, 1, t.icon)
	t.statusEdit, _, err = desktopUser.NewProc("CreateWindowExW").Call(0x200,
		uintptr(unsafe.Pointer(nativeText("EDIT"))), uintptr(unsafe.Pointer(nativeText("Loading status..."))),
		0x50000000|0x00200000|0x00010000|0x0004|0x0040|0x0800, // CHILD | VISIBLE | VSCROLL | TABSTOP | MULTILINE | AUTOVSCROLL | READONLY
		0, 0, 0, 0, hwnd, 1, 0, 0)
	if t.statusEdit == 0 {
		userCall("DestroyWindow", hwnd)
		return fmt.Errorf("create status text: %w", err)
	}
	// Read-only text must not silently truncate long Worker reports at the
	// native edit control's default limit. Vertical scrolling wraps long lines.
	userCall("SendMessageW", t.statusEdit, 0x00c5, 0x7ffffffe, 0) // EM_SETLIMITTEXT
	t.statusButton, _, err = desktopUser.NewProc("CreateWindowExW").Call(0,
		uintptr(unsafe.Pointer(nativeText("BUTTON"))), uintptr(unsafe.Pointer(nativeText("Close"))),
		0x50010001, 0, 0, 0, 0, hwnd, statusClose, 0, 0) // CHILD | VISIBLE | TABSTOP | DEFPUSHBUTTON
	if t.statusButton == 0 {
		userCall("DestroyWindow", hwnd)
		return fmt.Errorf("create status button: %w", err)
	}
	t.setStatusFont()
	t.layoutStatus()
	return nil
}

func (t *windowsTray) statusDPI() uintptr {
	dpi := userCall("GetDpiForWindow", t.statusWindow)
	if dpi == 0 {
		return 96
	}
	return dpi
}

func (t *windowsTray) setStatusFont() {
	height := -int32(10 * t.statusDPI() / 72)
	font, _, _ := desktopGDI.NewProc("CreateFontW").Call(uintptr(height), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(nativeText("Segoe UI"))))
	if font == 0 {
		return
	}
	userCall("SendMessageW", t.statusEdit, 0x0030, font, 1) // WM_SETFONT
	userCall("SendMessageW", t.statusButton, 0x0030, font, 1)
	if t.statusFont != 0 {
		_, _, _ = desktopGDI.NewProc("DeleteObject").Call(t.statusFont)
	}
	t.statusFont = font
}

func (t *windowsTray) layoutStatus() {
	var rect nativeRect
	userCall("GetClientRect", t.statusWindow, uintptr(unsafe.Pointer(&rect)))
	scale := func(value int32) int32 { return value * int32(t.statusDPI()) / 96 }
	margin, gap, buttonWidth, buttonHeight := scale(16), scale(12), scale(88), scale(30)
	width, height := rect.Right, rect.Bottom
	userCall("MoveWindow", t.statusEdit, uintptr(margin), uintptr(margin), uintptr(max(1, width-2*margin)), uintptr(max(1, height-2*margin-gap-buttonHeight)), 1)
	userCall("MoveWindow", t.statusButton, uintptr(max(margin, width-margin-buttonWidth)), uintptr(max(margin, height-margin-buttonHeight)), uintptr(buttonWidth), uintptr(buttonHeight), 1)
}

func (t *windowsTray) handleStatus(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmSize:
		t.layoutStatus()
		return 0
	case wmDPIChanged:
		// lParam points into Win32-owned memory, not the Go heap. Copy it
		// through the Windows API rather than retaining a foreign Go pointer.
		var rect nativeRect
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), lp, (*byte)(unsafe.Pointer(&rect)), unsafe.Sizeof(rect), nil); err == nil {
			userCall("SetWindowPos", hwnd, 0, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right-rect.Left), uintptr(rect.Bottom-rect.Top), 0x14) // NOZORDER | NOACTIVATE
		}
		t.setStatusFont()
		t.layoutStatus()
		return 0
	case wmCommand:
		if uint16(wp) != statusClose {
			break
		}
		fallthrough
	case wmClose:
		userCall("DestroyWindow", hwnd)
		return 0
	case wmDestroy:
		if t.statusCancel != nil {
			t.statusCancel()
			t.statusCancel = nil
		}
		t.statusGeneration++
		desktopWindows.Delete(hwnd)
		t.statusWindow, t.statusEdit, t.statusButton = 0, 0, 0
		if t.statusFont != 0 {
			_, _, _ = desktopGDI.NewProc("DeleteObject").Call(t.statusFont)
			t.statusFont = 0
		}
		return 0
	}
	return userCall("DefWindowProcW", hwnd, uintptr(msg), wp, lp)
}
