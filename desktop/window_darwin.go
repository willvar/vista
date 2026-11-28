package main

import (
	"errors"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const executableSuffix = ""

type desktopState struct{}

func startupError(err error) { showMacStartupError(err.Error()) }

func (d *desktop) platformOptions(options *application.Options) error {
	// Keep the Dock as a recovery path even when the menu bar is crowded.
	options.Mac.ApplicationShouldTerminateAfterLastWindowClosed = true
	return nil
}

func (d *desktop) setup(backend string, status serviceStatus) {
	d.setupTray(backend, status)
	if d.tray.created {
		d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
			application.InvokeAsync(func() { d.trayAvailability(true) })
		})
	}
	d.app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(_ *application.ApplicationEvent) {
		application.InvokeAsync(d.showHome)
	})
}

func (d *desktop) open(address string) *application.WebviewWindow {
	options := windowOptions()
	options.URL, options.Hidden = "about:blank", true
	w := d.app.Window.NewWithOptions(options)
	w.RegisterHook(events.Mac.WindowShouldClose, func(event *application.WindowEvent) { d.hideOnClose(w, event) })
	var once sync.Once
	w.OnWindowEvent(events.Mac.WebViewDidFinishNavigation, func(_ *application.WindowEvent) {
		once.Do(func() {
			application.InvokeSync(func() {
				if !attachMacNavigation(w.NativeWindow(), d) {
					d.err = errors.New("could not attach the WKWebView navigation policy")
					d.app.Quit()
					return
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
