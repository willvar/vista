package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed vista.png
var vistaIcon []byte

type trayState struct {
	ready, created, quitting, controlling bool
	companion                             string
	menu                                  *application.Menu
	controls                              [2]*application.MenuItem
}

// Do not create a second icon when a separately installed (possibly older)
// service is already running. Still open its WebUI, without owning its lifecycle.
func (d *desktop) setupTray(executable string, status serviceStatus) {
	if d.dir == "" || !matchesExecutable(executable, status.BinaryHash) {
		return
	}
	d.tray.created = true
	d.tray.companion = status.BinaryHash
	menu := d.app.NewMenu()
	item := func(label string, fn func()) *application.MenuItem {
		return menu.Add(label).OnClick(func(*application.Context) { application.InvokeAsync(fn) })
	}
	item("Open Vista", d.showHome)
	item("Status", d.showStatus)
	d.tray.menu = menu
	d.tray.controls = [2]*application.MenuItem{
		item("Restart", func() { d.confirmControl("Restart") }).SetEnabled(d.dir != ""),
		item("Stop", func() { d.confirmControl("Stop") }).SetEnabled(d.dir != ""),
	}
	menu.AddSeparator()
	if d.loginExecutable != "" {
		d.addLoginItem(menu)
	}
	item("Quit desktop", d.quitDesktop)
	tray := d.app.SystemTray.New().SetIcon(vistaIcon).SetMenu(menu)
	tray.SetTooltip("Vista")
	tray.OnClick(func() { application.InvokeAsync(d.showHome) })
	tray.OnDoubleClick(func() { application.InvokeAsync(d.showHome) })
	if d.startInTray {
		d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
			time.AfterFunc(2*time.Second, func() {
				application.InvokeAsync(func() {
					if d.startInTray && !d.tray.ready {
						d.showHome()
					}
				})
			})
		})
	}
}

func matchesExecutable(path, want string) bool {
	if len(want) != sha256.Size*2 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == want
}

// An unavailable tray must never be the only way back to a hidden window.
func (d *desktop) trayAvailability(ready bool) {
	lost := d.tray.ready && !ready
	d.tray.ready = ready
	d.originChanged()
	if lost && d.home != nil && !d.home.IsVisible() {
		d.showHome()
	}
}

func (d *desktop) hideOnClose(w *application.WebviewWindow, event *application.WindowEvent) {
	application.InvokeSync(func() {
		if w == d.home && d.tray.ready && !d.tray.quitting {
			event.Cancel()
			w.Hide()
		}
	})
}

func (d *desktop) showHome() {
	d.startInTray = false
	if d.home == nil || d.tray.quitting {
		return
	}
	d.home.Show()
	d.home.UnMinimise()
	d.home.Focus()
}

func (d *desktop) quitDesktop() {
	d.tray.quitting = true
	d.app.Quit()
}

// Only the window that can actually use the locally registered tray loses the
// WebUI restart button. Browser clients see no extra server state or requests.
func (d *desktop) originChanged() {
	origin := ""
	if d.tray.ready && d.dir != "" && d.base != nil {
		origin = d.base.Scheme + "://" + d.base.Host
	}
	if d.flags["vistaRestartTrayOrigin"] == origin {
		return
	}
	d.flags["vistaRestartTrayOrigin"] = origin
	encoded, _ := json.Marshal(origin)
	script := `if(window._wails?.flags){window._wails.flags.vistaRestartTrayOrigin=` + string(encoded) + `;window.dispatchEvent(new Event("wails:runtime-config-ready"));}`
	for _, view := range d.app.Window.GetAll() {
		view.ExecJS(script)
	}
}

func (d *desktop) showStatus() {
	base := d.base
	go func() {
		status, err := readStatus(base)
		if err != nil {
			d.app.Dialog.Warning().SetTitle("Vista Status").SetMessage("Could not read the connected service.\n" + err.Error()).Show()
			return
		}
		d.app.Dialog.Info().SetTitle("Vista Status").SetMessage(statusText(base, status)).Show()
	}()
}

func statusText(base *url.URL, s serviceStatus) string {
	return fmt.Sprintf("Running: %s\nVista %s · %s\nPID: %d   Uptime: %ds\nOpen conversations: %d   Running: %d\nWorkers: %d   Background processes: %d\nMemory: %.1f MiB\nDefault model: %s",
		base, s.Version, s.HostOS, s.PID, s.Uptime, s.Sessions.Open, s.Sessions.Running, s.Sessions.Workers, s.Jobs, float64(s.Memory.RSS)/(1<<20), s.DefaultModel)
}

func (d *desktop) confirmControl(label string) {
	if d.dir == "" || d.tray.controlling || d.tray.quitting {
		return
	}
	action := strings.ToLower(label)
	question := d.app.Dialog.Question().SetTitle(label + " Vista").SetMessage(label + " the connected Vista service? Running work and background processes may be interrupted.")
	// Yes/No preserves the safe default in the native MessageBox fallback too.
	question.AddButton("No").SetAsCancel().SetAsDefault()
	question.AddButton("Yes").OnClick(func() { application.InvokeAsync(func() { d.control(action) }) })
	question.Show()
}

func (d *desktop) setControlling(busy bool) {
	d.tray.controlling = busy
	for _, item := range d.tray.controls {
		item.SetEnabled(!busy && d.dir != "")
	}
	d.tray.menu.Update()
}

func (d *desktop) control(action string) {
	if d.dir == "" || d.tray.controlling || d.tray.quitting {
		return
	}
	d.setControlling(true)
	base, dir, expected := d.base, d.dir, d.tray.companion
	go func() {
		err := lifecycle(base, dir, expected, action)
		application.InvokeAsync(func() {
			d.setControlling(false)
			if d.tray.quitting {
				return
			}
			if err != nil {
				d.app.Dialog.Error().SetTitle("Vista").SetMessage(err.Error()).Show()
			} else if action == "stop" {
				d.quitDesktop()
				// Restart keeps the same workspace and lets its normal SSE recovery run.
			}
		})
	}()
}
