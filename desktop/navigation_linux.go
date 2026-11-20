package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <stdint.h>
#include <webkit2/webkit2.h>

extern int vistaNavigate(uintptr_t id, char *uri, int popup, int clicked);
extern void vistaReleaseNavigation(uintptr_t id);

static WebKitWebView *find_view(GtkWidget *widget) {
    if (WEBKIT_IS_WEB_VIEW(widget)) return WEBKIT_WEB_VIEW(widget);
    if (!GTK_IS_CONTAINER(widget)) return NULL;
    GList *children = gtk_container_get_children(GTK_CONTAINER(widget));
    WebKitWebView *view = NULL;
    for (GList *item = children; item && !view; item = item->next)
        view = find_view(GTK_WIDGET(item->data));
    g_list_free(children);
    return view;
}

static gboolean decide_policy(WebKitWebView *view, WebKitPolicyDecision *decision,
                              WebKitPolicyDecisionType type, gpointer data) {
    if (type != WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION &&
        type != WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) return FALSE;
    WebKitNavigationAction *action = webkit_navigation_policy_decision_get_navigation_action(
        WEBKIT_NAVIGATION_POLICY_DECISION(decision));
    WebKitURIRequest *request = webkit_navigation_action_get_request(action);
    const char *uri = request ? webkit_uri_request_get_uri(request) : NULL;
    gboolean popup = type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION;
    gboolean clicked = webkit_navigation_action_is_user_gesture(action);
    if (uri && vistaNavigate((uintptr_t)data, (char *)uri, popup, clicked))
        webkit_policy_decision_use(decision);
    else
        webkit_policy_decision_ignore(decision);
    return TRUE;
}

static void release_policy(gpointer data, GClosure *closure) {
    vistaReleaseNavigation((uintptr_t)data);
}

static int attach_policy(void *window, uintptr_t id) {
    if (!window) return 0;
    WebKitWebView *view = find_view(GTK_WIDGET(window));
    if (!view) return 0;
    // Vista uses HTTP/SSE only, not the framework's native message channel.
    WebKitUserContentManager *manager = webkit_web_view_get_user_content_manager(view);
    webkit_user_content_manager_unregister_script_message_handler(manager, "external");
    return g_signal_connect_data(view, "decide-policy", G_CALLBACK(decide_policy),
                                 (gpointer)id, release_policy, 0) != 0;
}
*/
import "C"

import (
	"runtime/cgo"
	"unsafe"
)

// Wails v3.1.0 exposes the GTK window but not WebKitGTK's navigation decision.
func attachNavigation(window unsafe.Pointer, d *desktop) bool {
	handle := cgo.NewHandle(d)
	if C.attach_policy(window, C.uintptr_t(handle)) == 0 {
		handle.Delete()
		return false
	}
	return true
}

//export vistaNavigate
func vistaNavigate(id C.uintptr_t, uri *C.char, popup, clicked C.int) C.int {
	d := cgo.Handle(id).Value().(*desktop)
	if d.navigate(C.GoString(uri), popup != 0, clicked != 0) {
		return 1
	}
	return 0
}

//export vistaReleaseNavigation
func vistaReleaseNavigation(id C.uintptr_t) {
	cgo.Handle(id).Delete()
}
