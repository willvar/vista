package main

import (
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

const executableSuffix = ".exe"

type desktopState struct{}

func (d *desktop) platformOptions(options *application.Options) error {
	root, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	options.Icon = vistaIcon
	options.Windows.WebviewUserDataPath = filepath.Join(root, "vista-desktop", "webview2")
	options.Windows.TrayAvailabilityChanged = d.trayAvailability
	options.Assets = application.AssetOptions{} // No bound methods or custom asset transport.
	return nil
}

func (d *desktop) open(address string) *application.WebviewWindow {
	options := windowOptions()
	options.URL, options.Hidden = address, d.home == nil && d.startInTray
	options.Windows.NavigationPolicy = d.navigate
	options.Permissions[application.PermissionGeolocation] = application.PermissionDeny
	options.Permissions[application.PermissionNotifications] = application.PermissionDeny
	w := d.app.Window.NewWithOptions(options)
	w.RegisterHook(events.Windows.WindowClosing, func(event *application.WindowEvent) { d.hideOnClose(w, event) })
	return w
}

func (d *desktop) setup(backend string, status serviceStatus) { d.setupTray(backend, status) }

func startupError(err error) {
	text, _ := windows.UTF16PtrFromString(err.Error())
	title, _ := windows.UTF16PtrFromString("Vista Desktop")
	_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
}
