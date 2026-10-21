package platform

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func HostOS() string {
	name := "Linux"
	version := ""
	var info unix.Utsname
	if unix.Uname(&info) == nil {
		version = unix.ByteSliceToString(info.Release[:])
		if suffix := strings.IndexAny(version, "-+"); suffix >= 0 {
			version = version[:suffix]
		}
	}
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		data, err := os.ReadFile(path)
		if err == nil {
			if distro := osReleaseName(string(data)); distro != "" {
				name = distro
			}
			break
		}
		if !os.IsNotExist(err) {
			break
		}
	}
	if version != "" {
		return name + " · " + version
	}
	return name
}

func osReleaseName(data string) string {
	values := make(map[string]string)
	for line := range strings.SplitSeq(data, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (key != "PRETTY_NAME" && key != "NAME" && key != "VERSION_ID") {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == value[len(value)-1] {
			switch value[0] {
			case '"':
				value = strings.NewReplacer(`\"`, `"`, `\$`, `$`, "\\`", "`", `\\`, `\`).Replace(value[1 : len(value)-1])
			case '\'':
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	if values["PRETTY_NAME"] != "" {
		return values["PRETTY_NAME"]
	}
	return strings.TrimSpace(values["NAME"] + " " + values["VERSION_ID"])
}
