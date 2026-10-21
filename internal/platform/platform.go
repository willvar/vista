package platform

import (
	"errors"
	"io/fs"
	"runtime"
	"sync/atomic"
)

var ErrUnsupported = errors.New("Vista supports Linux, Windows, and macOS")
var ErrIdentityMismatch = errors.New("process identity changed")

// Windows exposes DOS attributes through FileMode, not POSIX access bits.
// Configuration there uses the access controls inherited from the user profile.
func PrivateMode(mode fs.FileMode) bool {
	return runtime.GOOS == "windows" || mode.Perm()&0077 == 0
}

func ShellName() string {
	if runtime.GOOS == "windows" {
		return "Windows PowerShell"
	}
	return "/bin/sh (POSIX)"
}

var observedPeakRSS atomic.Uint64

func recordPeakRSS(value uint64) uint64 {
	for {
		peak := observedPeakRSS.Load()
		if value <= peak {
			return peak
		}
		if observedPeakRSS.CompareAndSwap(peak, value) {
			return value
		}
	}
}
