package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Do not create a second icon when a separately installed (possibly older)
// service is already running. Still open its WebUI, without owning its lifecycle.
func (d *desktop) setup(backend string, status serviceStatus) {
	if d.dir == "" || !matchesCompanion(backend, status.BinaryHash) {
		return
	}
	d.trayCompanion = status.BinaryHash
	menu := d.app.NewMenu()
	item := func(label string, fn func()) *application.MenuItem {
		return menu.Add(label).OnClick(func(*application.Context) { application.InvokeAsync(fn) })
	}
	item("Open Vista", d.showHome)
	item("Status", d.showStatus)
	d.menu = menu
	d.controls = [2]*application.MenuItem{
		item("Restart", func() { d.confirmControl("Restart") }).SetEnabled(d.dir != ""),
		item("Stop", func() { d.confirmControl("Stop") }).SetEnabled(d.dir != ""),
	}
	menu.AddSeparator()
	item("Quit desktop", d.quitDesktop)
	tray := d.app.SystemTray.New().SetIcon(vistaIcon).SetMenu(menu)
	tray.SetTooltip("Vista")
	tray.OnClick(func() { application.InvokeAsync(d.showHome) })
	tray.OnDoubleClick(func() { application.InvokeAsync(d.showHome) })
}

func matchesCompanion(backend, want string) bool {
	if len(want) != sha256.Size*2 {
		return false
	}
	path, err := companionPath(backend)
	if err != nil {
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

func (d *desktop) quitDesktop() {
	d.quitting = true
	d.app.Quit()
}

// Only the window that can actually use the locally registered tray loses the
// WebUI restart button. Browser clients see no extra server state or requests.
func (d *desktop) originChanged() {
	origin := ""
	if d.trayReady && d.dir != "" && d.base != nil {
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
	if d.dir == "" || d.controlling || d.quitting {
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
	d.controlling = busy
	for _, item := range d.controls {
		item.SetEnabled(!busy && d.dir != "")
	}
	d.menu.Update()
}

func (d *desktop) control(action string) {
	if d.dir == "" || d.controlling || d.quitting {
		return
	}
	d.setControlling(true)
	base, dir, expected := d.base, d.dir, d.trayCompanion
	go func() {
		err := lifecycle(base, dir, expected, action)
		application.InvokeAsync(func() {
			d.setControlling(false)
			if d.quitting {
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
