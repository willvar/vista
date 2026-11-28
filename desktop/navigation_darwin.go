package main

/*
#cgo CFLAGS: -fblocks
#cgo LDFLAGS: -framework AppKit -framework WebKit
#include <stdint.h>
#include <stdlib.h>
int vistaAttachMacNavigation(void *window, uintptr_t handle);
void vistaShowMacStartupError(char *message);
*/
import "C"

import (
	"runtime"
	"runtime/cgo"
	"unsafe"
)

func attachMacNavigation(window unsafe.Pointer, d *desktop) bool {
	h := cgo.NewHandle(d)
	if C.vistaAttachMacNavigation(window, C.uintptr_t(h)) == 0 {
		h.Delete()
		return false
	}
	return true
}

//export vistaMacNavigate
func vistaMacNavigate(id C.uintptr_t, uri *C.char, popup, clicked C.int) C.int {
	d := cgo.Handle(id).Value().(*desktop)
	if d.navigate(C.GoString(uri), popup != 0, clicked != 0) {
		return 1
	}
	return 0
}

//export vistaReleaseMacNavigation
func vistaReleaseMacNavigation(id C.uintptr_t) { cgo.Handle(id).Delete() }

func showMacStartupError(message string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	s := C.CString(message)
	defer C.free(unsafe.Pointer(s))
	C.vistaShowMacStartupError(s)
}
