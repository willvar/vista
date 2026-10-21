package platform

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func HostOS() string {
	info := windows.RtlGetVersion()
	name := "Windows"
	if info.ProductType != 1 { // VER_NT_WORKSTATION
		name = "Windows Server"
	} else if info.MajorVersion == 10 {
		if info.BuildNumber >= 22000 {
			name = "Windows 11"
		} else {
			name = "Windows 10"
		}
	}
	return fmt.Sprintf("%s (%d.%d.%d)", name, info.MajorVersion, info.MinorVersion, info.BuildNumber)
}
