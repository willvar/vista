package main

import (
	"errors"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"sync"
)

func (d *desktop) platformOptions(options *application.Options) error {
	options.Linux.ApplicationID = "io.github.willvar.VistaDesktop"
	return nil
}

func (d *desktop) setup(_ string, _ serviceStatus) {
	d.app.Event.OnApplicationEvent(events.Linux.SystemThemeChanged, func(_ *application.ApplicationEvent) {
		application.InvokeAsync(d.syncSystemAppearance)
	})
}

const executableSuffix = ""

type desktopState struct{ appearanceInitialized bool }

func startupError(error)          {}
func (d *desktop) originChanged() {}

func (d *desktop) open(address string) *application.WebviewWindow {
	options := windowOptions()
	options.URL, options.Hidden = "about:blank", true
	w := d.app.Window.NewWithOptions(options)
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
				w.Show()
			})
		})
	})
	return w
}
