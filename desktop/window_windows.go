package main

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

const executableSuffix = ".exe"

//go:embed vista.png
var vistaIcon []byte

type desktopState struct {
	trayReady, quitting, controlling bool // UI thread only.
	trayCompanion                    string
	menu                             *application.Menu
	controls                         [2]*application.MenuItem
}

func (d *desktop) platformOptions(options *application.Options) error {
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(root) {
		return errors.New("the user configuration directory must be absolute")
	}
	options.Icon = vistaIcon
	options.Windows.WebviewUserDataPath = filepath.Join(root, "vista-desktop", "webview2")
	options.Windows.TrayAvailabilityChanged = func(ready bool) {
		d.trayReady = ready
		d.originChanged()
		if !ready && !d.quitting && d.home != nil && !d.home.IsVisible() {
			d.home.Show()
		}
	}
	options.SingleInstance = &application.SingleInstanceOptions{
		UniqueID:               fmt.Sprintf("io.github.willvar.VistaDesktop.%x", sha256.Sum256([]byte(root))),
		OnSecondInstanceLaunch: func(application.SecondInstanceData) { application.InvokeAsync(d.showHome) },
	}
	options.Assets = application.AssetOptions{} // No bound methods or custom asset transport.
	return nil
}

func (d *desktop) open(address string) *application.WebviewWindow {
	options := windowOptions()
	options.URL = address
	options.Windows.NavigationPolicy = d.navigate
	options.Permissions[application.PermissionGeolocation] = application.PermissionDeny
	options.Permissions[application.PermissionNotifications] = application.PermissionDeny
	w := d.app.Window.NewWithOptions(options)
	w.RegisterHook(events.Windows.WindowClosing, func(event *application.WindowEvent) {
		if w == d.home && d.trayReady && !d.quitting {
			event.Cancel()
			w.Hide()
		}
	})
	return w
}

func (d *desktop) showHome() {
	if d.home == nil || d.quitting {
		return
	}
	d.home.Show()
	d.home.UnMinimise()
	d.home.Focus()
}

func startupError(err error) {
	text, _ := windows.UTF16PtrFromString(err.Error())
	title, _ := windows.UTF16PtrFromString("Vista Desktop")
	_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
}
