package provider

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func retryableModelConnectionError(errno syscall.Errno) bool {
	switch errno {
	case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE,
		windows.WSAECONNREFUSED, windows.WSAECONNRESET, windows.WSAECONNABORTED,
		windows.ERROR_NETNAME_DELETED, windows.ERROR_BROKEN_PIPE:
		return true
	default:
		return false
	}
}
