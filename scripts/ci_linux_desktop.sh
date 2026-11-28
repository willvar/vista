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
    if command -v kded6 >/dev/null; then kded=kded6; else kded=kded5; fi
    "$kded" >"$VISTA_DISPLAY_LOG/kded.log" 2>&1 & trayhost=$!
    plasmashell --no-respawn >"$VISTA_DISPLAY_LOG/panel.log" 2>&1 & panel=$!
    attempt=0
    until busctl --user --timeout=1s get-property org.kde.StatusNotifierWatcher /StatusNotifierWatcher \
        org.kde.StatusNotifierWatcher IsStatusNotifierHostRegistered 2>/dev/null | grep -q 'b true'; do
        attempt=$((attempt + 1))
        if [ "$attempt" -ge 120 ] || ! kill -0 "$panel" 2>/dev/null; then
            tail -40 "$VISTA_DISPLAY_LOG/"*.log >&2; exit 1
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
    tail -60 "$sandbox/bus.log" >&2; exit 1
}
