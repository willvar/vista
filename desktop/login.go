package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Use fixed, owned login entries rather than the Wails manager's discovery
// by executable. Enabling must not bootstrap another app and disabling must
// not unload the currently running desktop (e.g. a macOS LaunchAgent).
const desktopID = "io.github.willvar.VistaDesktop"

func (d *desktop) addLoginItem(menu *application.Menu) {
	const label = "Launch at login"
	current, err := loginEnabled(d.loginExecutable)
	entry := menu.AddCheckbox(label, current)
	if err != nil {
		entry.SetEnabled(false)
		d.app.Logger.Warn("Could not read Vista's login setting", "error", err)
	}
	entry.OnClick(func(_ *application.Context) {
		application.InvokeAsync(func() {
			// Wails optimistically ticks the item on click. Re-read OS state both
			// before making a change and before displaying the resulting state.
			previous, err := loginEnabled(d.loginExecutable)
			if err == nil {
				err = setLogin(d.loginExecutable, !previous)
			}
			actual, readErr := loginEnabled(d.loginExecutable)
			entry.SetChecked(actual).SetEnabled(readErr == nil)
			menu.Update()
			if err = errors.Join(err, readErr); err != nil {
				d.app.Dialog.Error().SetTitle("Vista").SetMessage(fmt.Sprintf("Could not change the desktop login setting.\n%s", err)).Show()
			}
		})
	})
}

// File-based login items are private to the user. Never replace or remove an
// unexpected file, including a symlink, at Vista's reserved entry path.
type loginFile struct{ path, marker string }

func (f loginFile) enabled() (bool, error) {
	info, err := os.Lstat(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 32*1024 {
		return false, errors.New("unexpected login item at " + f.path)
	}
	file, err := os.Open(f.path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, 32*1024+1))
	if err != nil {
		return false, err
	}
	if len(b) > 32*1024 || !strings.Contains(string(b), f.marker) {
		return false, errors.New("login item is not owned by Vista: " + f.path)
	}
	return true, nil
}

func (f loginFile) set(enable bool, content []byte) error {
	existing, err := f.enabled()
	if err != nil {
		return err
	}
	if !enable {
		if existing {
			return os.Remove(f.path)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0700); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(f.path), ".vista-login-")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	if _, err := out.Write(content); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), f.path)
}
