package platform

import "golang.org/x/sys/unix"

func HostOS() string {
	if version, err := unix.Sysctl("kern.osproductversion"); err == nil && version != "" {
		return "macOS · " + version
	}
	if version, err := unix.Sysctl("kern.osrelease"); err == nil && version != "" {
		return "macOS · Darwin " + version
	}
	return "macOS"
}
