package main

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func macLoginFile() (loginFile, error) {
	home, err := os.UserHomeDir()
	return loginFile{filepath.Join(home, "Library", "LaunchAgents", desktopID+".login.plist"),
		"<string>" + desktopID + ".login</string>"}, err
}

func loginEnabled(_ string) (bool, error) {
	f, err := macLoginFile()
	if err != nil {
		return false, err
	}
	return f.enabled()
}

func setLogin(executable string, enable bool) error {
	f, err := macLoginFile()
	if err != nil {
		return err
	}
	if !enable {
		return f.set(false, nil)
	}
	if !filepath.IsAbs(executable) || !strings.HasSuffix(filepath.Dir(executable), ".app/Contents/MacOS") {
		return errors.New("launch at login requires Vista to be installed as an application bundle")
	}
	var path strings.Builder
	if err := xml.EscapeText(&path, []byte(executable)); err != nil {
		return err
	}
	entry := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key>` + f.marker + `
<key>ProgramArguments</key><array><string>` + path.String() + `</string><string>--tray</string></array>
<key>RunAtLoad</key><true/>
<key>LimitLoadToSessionType</key><string>Aqua</string>
</dict></plist>
`
	// LaunchAgents are picked up at the next GUI login, not launched now.
	return f.set(true, []byte(entry))
}
