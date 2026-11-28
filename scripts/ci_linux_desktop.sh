#!/bin/sh
# Run the native application checks with an isolated real KDE tray and Wayland compositor.
set -eu
if [ "${1:-}" = --session ]; then
    shift
    cleanup() {
        for pid in "${panel:-}" "${trayhost:-}" "${wm:-}"; do
            if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || :; fi
        done
        wait
    }
    trap cleanup EXIT
    kwin_wayland --virtual --socket "$WAYLAND_DISPLAY" --width 1024 --height 768 --no-lockscreen \
        >"$VISTA_DISPLAY_LOG/kwin.log" 2>&1 & wm=$!
    attempt=0
    until [ -S "$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY" ]; do
        attempt=$((attempt + 1))
        if [ "$attempt" -ge 60 ] || ! kill -0 "$wm" 2>/dev/null; then
            cat "$VISTA_DISPLAY_LOG/kwin.log" >&2; exit 1
        fi
        sleep 0.25
    done
    dbus-update-activation-environment WAYLAND_DISPLAY XDG_RUNTIME_DIR XDG_SESSION_TYPE QT_QPA_PLATFORM
    if command -v kded6 >/dev/null; then
        kded6 >"$VISTA_DISPLAY_LOG/kded.log" 2>&1 &
    else
        # KDED5 autoloads the phase-one StatusNotifierWatcher only in a KDE 5 session.
        KDE_FULL_SESSION=true KDE_SESSION_VERSION=5 KDE_SESSION_UID="$(id -u)" \
            kded5 >"$VISTA_DISPLAY_LOG/kded.log" 2>&1 &
    fi
    trayhost=$!
    # Plasma 5 can abort its initial layout while ActivityManager is starting.
    busctl --address="$DBUS_SESSION_BUS_ADDRESS" --timeout=8s call org.freedesktop.DBus /org/freedesktop/DBus \
        org.freedesktop.DBus StartServiceByName su org.kde.ActivityManager 0 >"$VISTA_DISPLAY_LOG/activity.log" 2>&1
    plasmashell --no-respawn >"$VISTA_DISPLAY_LOG/panel.log" 2>&1 & panel=$!
    attempt=0
    tray_added=0
    until busctl --address="$DBUS_SESSION_BUS_ADDRESS" --timeout=1s get-property org.kde.StatusNotifierWatcher /StatusNotifierWatcher \
        org.kde.StatusNotifierWatcher IsStatusNotifierHostRegistered 2>/dev/null | grep -q 'b true'; do
        attempt=$((attempt + 1))
        if [ "$attempt" -ge 120 ] || ! kill -0 "$panel" 2>/dev/null; then
            busctl --address="$DBUS_SESSION_BUS_ADDRESS" --timeout=1s list >"$VISTA_DISPLAY_LOG/services.log" 2>&1 || :
            printf "%s\n" "The private KDE tray host did not become available." >&2; exit 1
        fi
        # Minimal runner installations need not put a tray in the default
        # panel. Add the real Plasma tray to this private session if needed.
        if [ "$attempt" -ge 8 ] && [ "$tray_added" -eq 0 ]; then
            if busctl --address="$DBUS_SESSION_BUS_ADDRESS" --timeout=2s call org.kde.plasmashell /PlasmaShell \
                org.kde.PlasmaShell evaluateScript s 'var panel = new Panel; panel.addWidget("org.kde.plasma.systemtray");' \
                >>"$VISTA_DISPLAY_LOG/tray-setup.log" 2>&1; then
                tray_added=1
            fi
        fi
        sleep 0.25
    done
    printf '%s\n' 'Isolated KDE Wayland compositor and StatusNotifierItem host ready.'
    python3 -u scripts/ci_native.py "$@"
    exit
fi

umask 077
mkdir -p .vista/ci
sandbox=$(mktemp -d "$PWD/.vista/ci/linux-desktop.XXXXXXXX")
# Keep the Wayland socket path short even in a long worktree path.
runtime=$(mktemp -d /tmp/vista-wl.XXXXXXXX)
trap 'rm -rf -- "$sandbox" "$runtime"' EXIT
mkdir -p "$sandbox/config" "$sandbox/data" "$sandbox/cache" "$sandbox/state"
unset DBUS_SESSION_BUS_ADDRESS DISPLAY WAYLAND_SOCKET SESSION_MANAGER KDE_FULL_SESSION
export VISTA_DISPLAY_LOG="$sandbox" XDG_RUNTIME_DIR="$runtime" WAYLAND_DISPLAY=vista-test
export XDG_CONFIG_HOME="$sandbox/config" XDG_DATA_HOME="$sandbox/data" XDG_CACHE_HOME="$sandbox/cache" XDG_STATE_HOME="$sandbox/state"
# Keep application display state isolated but reuse the configured Go cache.
export GOCACHE="$(GOWORK=off go env GOCACHE)"
export QT_QPA_PLATFORM=wayland GDK_BACKEND=wayland XDG_SESSION_TYPE=wayland XDG_CURRENT_DESKTOP=KDE LIBGL_ALWAYS_SOFTWARE=1
# Start the shell and its tray watcher only on the private bus.
dbus-run-session -- sh "$0" --session "$@" 2>"$sandbox/bus.log" || {
    # Preserve private-session diagnostics before the EXIT trap removes them.
    mkdir -p .vista/ci/linux-display-logs
    for log in "$sandbox/"*.log; do
        [ -f "$log" ] || continue
        cp "$log" .vista/ci/linux-display-logs/
        printf '\n--- %s ---\n' "${log##*/}" >&2
        tail -n 60 "$log" >&2
    done
    exit 1
}
