package main

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Wails exposes the GTK window and theme changes, but not WebKitGTK's
// navigation decisions or GTK's theme property. Bind only those native calls.
// This is used on the GTK thread, never by the service or CLI dispatch.
var linuxGTK = sync.OnceValues(loadLinuxGTK)

const (
	webkitNavigationAction = iota
	webkitNewWindowAction
)

type linuxGTKAPI struct {
	webViewType, containerType func() uintptr
	isA                        func(unsafe.Pointer, uintptr) int32
	children                   func(unsafe.Pointer) *gtkList
	freeList                   func(*gtkList)
	contentManager             func(unsafe.Pointer) unsafe.Pointer
	unregister                 func(unsafe.Pointer, string)
	connect                    func(unsafe.Pointer, string, uintptr, unsafe.Pointer, uintptr, uint32) uintptr
	policyAction, request      func(unsafe.Pointer) unsafe.Pointer
	uri                        func(unsafe.Pointer) string
	userGesture                func(unsafe.Pointer) int32
	use, ignore                func(unsafe.Pointer)
	settings                   func() unsafe.Pointer
	valueInit                  func(*gtkValue, uintptr) *gtkValue
	valueSetBoolean            func(*gtkValue, int32)
	valueUnset                 func(*gtkValue)
	setProperty                func(unsafe.Pointer, string, *gtkValue)

	callbacks       sync.Once
	decide, release uintptr
	views           sync.Map // native WebKit view -> *desktop; removed by the signal closure's destroy callback
}

// GLib's GList and GValue layouts. The value is used only for a gboolean, and
// g_object_set_property copies it during the call instead of retaining it.
type gtkList struct {
	data unsafe.Pointer
	next *gtkList
}

type gtkValue struct {
	typeID uintptr
	data   [2]uint64
}

func loadLinuxGTK() (*linuxGTKAPI, error) {
	// WebKitGTK's dependencies include GTK3 and GLib. Keep the handle alive
	// for the lifetime of the callbacks; binding failure must fail closed.
	lib, err := purego.Dlopen("libwebkit2gtk-4.1.so.0", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	g := new(linuxGTKAPI)
	for name, fn := range map[string]any{
		"webkit_web_view_get_type":                                      &g.webViewType,
		"gtk_container_get_type":                                        &g.containerType,
		"g_type_check_instance_is_a":                                    &g.isA,
		"gtk_container_get_children":                                    &g.children,
		"g_list_free":                                                   &g.freeList,
		"webkit_web_view_get_user_content_manager":                      &g.contentManager,
		"webkit_user_content_manager_unregister_script_message_handler": &g.unregister,
		"g_signal_connect_data":                                         &g.connect,
		"webkit_navigation_policy_decision_get_navigation_action":       &g.policyAction,
		"webkit_navigation_action_get_request":                          &g.request,
		"webkit_uri_request_get_uri":                                    &g.uri,
		"webkit_navigation_action_is_user_gesture":                      &g.userGesture,
		"webkit_policy_decision_use":                                    &g.use,
		"webkit_policy_decision_ignore":                                 &g.ignore,
		"gtk_settings_get_default":                                      &g.settings,
		"g_value_init":                                                  &g.valueInit,
		"g_value_set_boolean":                                           &g.valueSetBoolean,
		"g_value_unset":                                                 &g.valueUnset,
		"g_object_set_property":                                         &g.setProperty,
	} {
		symbol, err := purego.Dlsym(lib, name)
		if err != nil {
			_ = purego.Dlclose(lib)
			return nil, fmt.Errorf("resolve %s: %w", name, err)
		}
		purego.RegisterFunc(fn, symbol)
	}
	return g, nil
}

func (g *linuxGTKAPI) findWebView(widget unsafe.Pointer) unsafe.Pointer {
	if widget == nil {
		return nil
	}
	if g.isA(widget, g.webViewType()) != 0 {
		return widget
	}
	if g.isA(widget, g.containerType()) == 0 {
		return nil
	}
	children := g.children(widget)
	defer g.freeList(children)
	for child := children; child != nil; child = child.next {
		if view := g.findWebView(child.data); view != nil {
			return view
		}
	}
	return nil
}

func attachNavigation(window unsafe.Pointer, d *desktop) bool {
	g, err := linuxGTK()
	if err != nil {
		return false
	}
	view := g.findWebView(window)
	if view == nil {
		return false
	}
	manager := g.contentManager(view)
	if manager == nil {
		return false
	}
	if _, exists := g.views.LoadOrStore(view, d); exists {
		return false
	}
	// Callbacks cannot be freed by purego. Use one pair for the process, not
	// one per view, and use native pointers rather than Go pointers as data.
	g.callbacks.Do(func() {
		g.decide = purego.NewCallback(g.decidePolicy)
		g.release = purego.NewCallback(func(view, _ unsafe.Pointer) { g.views.Delete(view) })
	})
	// The WebUI uses HTTP/SSE, not the framework's native message channel.
	g.unregister(manager, "external")
	if g.connect(view, "decide-policy", g.decide, view, g.release, 0) != 0 {
		return true
	}
	g.views.Delete(view)
	return false
}

func (g *linuxGTKAPI) decidePolicy(_, decision unsafe.Pointer, kind int32, view unsafe.Pointer) int32 {
	if kind != webkitNavigationAction && kind != webkitNewWindowAction {
		return 0 // Leave response decisions to WebKit and Wails.
	}
	if owner, ok := g.views.Load(view); ok {
		if action := g.policyAction(decision); action != nil {
			if request := g.request(action); request != nil {
				if uri := g.uri(request); uri != "" && owner.(*desktop).navigate(uri, kind == webkitNewWindowAction, g.userGesture(action) != 0) {
					g.use(decision)
					return 1
				}
			}
		}
	}
	g.ignore(decision)
	return 1
}

// Refresh the portal preference on the GTK thread, not from a stale event.
func (d *desktop) syncSystemAppearance() {
	g, err := linuxGTK()
	if err != nil {
		return
	}
	if settings := g.settings(); settings != nil {
		var value gtkValue
		// G_TYPE_BOOLEAN is GLib's fundamental type 5, shifted left by 2.
		const booleanType = 5 << 2
		g.valueInit(&value, booleanType)
		var dark int32
		if d.app.Env.IsDarkMode() {
			dark = 1
		}
		g.valueSetBoolean(&value, dark)
		g.setProperty(settings, "gtk-application-prefer-dark-theme", &value)
		g.valueUnset(&value)
	}
	d.appearanceInitialized = true
}
