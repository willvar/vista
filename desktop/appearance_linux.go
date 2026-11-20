package main

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>

static void set_prefer_dark(int dark) {
    GtkSettings *settings = gtk_settings_get_default();
    if (settings) g_object_set(settings, "gtk-application-prefer-dark-theme", dark, NULL);
}
*/
import "C"

// Refresh the portal preference on the GTK thread instead of applying stale
// values if Wails dispatches theme events concurrently. WebKitGTK then updates
// prefers-color-scheme; the WebUI still owns any explicit user override.
func (d *desktop) syncSystemAppearance() {
	var dark C.int
	if d.app.Env.IsDarkMode() {
		dark = 1
	}
	C.set_prefer_dark(dark)
	d.appearanceInitialized = true
}
