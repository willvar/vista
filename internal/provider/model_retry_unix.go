//go:build !windows

package provider

import "syscall"

func retryableModelConnectionError(errno syscall.Errno) bool {
	switch errno {
	case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE:
		return true
	default:
		return false
	}
}
