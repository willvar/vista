package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const watcher = "org.kde.StatusNotifierWatcher"

// Wails registers and re-registers the StatusNotifierItem; it does not expose
// the panel's availability. Observe the watcher instead of assuming that
// creating a Wails tray object gave the user a visible way back to the window.
func (d *desktop) monitorLinuxTray(ctx context.Context) {
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		return
	}
	defer bus.Close()
	signals := make(chan *dbus.Signal, 16)
	bus.Signal(signals)
	defer bus.RemoveSignal(signals)
	for _, match := range [][]dbus.MatchOption{
		{dbus.WithMatchInterface(watcher), dbus.WithMatchObjectPath("/StatusNotifierWatcher")},
		{dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, watcher)},
		{dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchObjectPath("/StatusNotifierWatcher")},
	} {
		if err := bus.AddMatchSignalContext(ctx, match...); err != nil {
			return
		}
	}
	report := func(ready bool) {
		if ctx.Err() == nil {
			application.InvokeAsync(func() {
				if ctx.Err() == nil {
					d.trayAvailability(ready)
				}
			})
		}
	}
	for {
		check, cancel := context.WithTimeout(ctx, time.Second)
		report(len(linuxTrayItems(check, bus, uint32(os.Getpid()))) > 0)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-bus.Context().Done():
			report(false)
			return
		case _, ok := <-signals:
			if !ok {
				report(false)
				return
			}
		}
	}
}

func linuxTrayItems(ctx context.Context, bus *dbus.Conn, pid uint32) []string {
	panel := bus.Object(watcher, "/StatusNotifierWatcher")
	var host, items dbus.Variant
	const get = "org.freedesktop.DBus.Properties.Get"
	if panel.CallWithContext(ctx, get, 0, watcher, "IsStatusNotifierHostRegistered").Store(&host) != nil || host.Value() != true {
		return nil
	}
	if panel.CallWithContext(ctx, get, 0, watcher, "RegisteredStatusNotifierItems").Store(&items) != nil {
		return nil
	}
	list, _ := items.Value().([]string)
	var found []string
	for _, item := range list {
		service, _, _ := strings.Cut(item, "/")
		if service == "" {
			continue
		}
		var owner uint32
		if bus.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, service).Store(&owner) == nil && owner == pid {
			found = append(found, item)
		}
	}
	return found
}
