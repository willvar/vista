package main

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type loginRunEntry struct{ key, name string }

var vistaRunEntry = loginRunEntry{`Software\Microsoft\Windows\CurrentVersion\Run`, desktopID}

func loginEnabled(_ string) (bool, error)           { return vistaRunEntry.enabled() }
func setLogin(executable string, enable bool) error { return vistaRunEntry.set(executable, enable) }

func (e loginRunEntry) enabled() (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, e.key, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer key.Close()
	value, _, err := key.GetStringValue(e.name)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return value != "", err
}

func (e loginRunEntry) set(executable string, enable bool) error {
	if enable {
		command := windows.ComposeCommandLine([]string{executable, "--tray"})
		utf16, err := windows.UTF16FromString(command)
		if err != nil {
			return err
		}
		if len(utf16) > 261 {
			return errors.New("Vista's installed path is too long for a Windows login item")
		}
		key, _, err := registry.CreateKey(registry.CURRENT_USER, e.key, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer key.Close()
		return key.SetStringValue(e.name, command)
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, e.key, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	err = key.DeleteValue(e.name)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}
