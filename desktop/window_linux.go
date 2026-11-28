package main

import (
	"context"
	"errors"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"os"
	"sync"
)

func (d *desktop) platformOptions(options *application.Options) error {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("WAYLAND_SOCKET") == "" {
		return errors.New("no graphical session; use vista serve to run without a desktop")
	}
	options.Linux.ApplicationID = desktopID
	return nil
}

func (d *desktop) setup(executable string, status serviceStatus) {
	d.setupTray(executable, status)
	if d.tray.created {
		ctx, cancel := context.WithCancel(context.Background())
		d.app.OnShutdown(cancel)
		d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
			go d.monitorLinuxTray(ctx)
		})
	}
	d.app.Event.OnApplicationEvent(events.Linux.SystemThemeChanged, func(_ *application.ApplicationEvent) {
		application.InvokeAsync(d.syncSystemAppearance)
	})
}

type desktopState struct{ appearanceInitialized bool }

func startupError(error) {}

func (d *desktop) open(address string) *application.WebviewWindow {
	options := windowOptions()
	options.URL, options.Hidden = "about:blank", true
	w := d.app.Window.NewWithOptions(options)
	w.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) { d.hideOnClose(w, event) })
	var once sync.Once
	w.OnWindowEvent(events.Linux.WindowLoadFinished, func(_ *application.WindowEvent) {
		once.Do(func() {
			application.InvokeSync(func() {
				// Attach before loading the service, not after any site can run.
				if !attachNavigation(w.NativeWindow(), d) {
					d.err = errors.New("could not attach the WebView navigation policy")
					d.app.Quit()
					return
				}
				if !d.appearanceInitialized {
					d.syncSystemAppearance()
				}
				w.SetURL(address)
				if w != d.home || !d.startInTray {
					w.Show()
				}
			})
		})
	})
	return w
}
