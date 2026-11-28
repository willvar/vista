package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func linuxLoginFile() (loginFile, error) {
	root, err := os.UserConfigDir()
	return loginFile{filepath.Join(root, "autostart", desktopID+".desktop"), "\nX-Vista-Desktop-Login=true\n"}, err
}

func desktopExec(path string, tray bool) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n\t") {
		return "", errors.New("desktop executable path cannot be represented in a desktop entry")
	}
	// The Exec grammar is not a shell. Escape quoting/field codes first, then
	// the resulting backslashes for the desktop-entry string-value grammar.
	quoted := `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`", "%", "%%").Replace(path) + `"`
	command := strings.ReplaceAll(quoted, `\`, `\\`)
	if tray {
		command += " --tray"
	}
	return command, nil
}

func loginEnabled(_ string) (bool, error) {
	f, err := linuxLoginFile()
	if err != nil {
		return false, err
	}
	return f.enabled()
}

func setLogin(executable string, enable bool) error {
	f, err := linuxLoginFile()
	if err != nil {
		return err
	}
	if !enable {
		return f.set(false, nil)
	}
	exec, err := desktopExec(executable, true)
	if err != nil {
		return err
	}
	entry := "[Desktop Entry]\nType=Application\nName=Vista\nExec=" + exec + "\nIcon=" + desktopID + "\nTerminal=false\n" + f.marker
	return f.set(true, []byte(entry))
}
