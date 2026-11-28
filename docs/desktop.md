# Native window and tray

[简体中文](./desktop.zh-CN.md)

The `vista` executable includes the local service, WebUI, CLI, and Wails desktop window and tray. The window and browser share the same configuration, conversations, HTTP API and event stream. The page cannot call Vista's native Go methods.

## Build and install

`make build` builds `bin/vista` (Windows: `bin/vista.exe`); `make package` creates one archive. Both use Python 3. All delivery builds use `CGO_ENABLED=0`. macOS packages place the executable at `Vista.app/Contents/MacOS/vista`.

| Platform | Runtime for a native window |
| --- | --- |
| Windows | WebView2 Runtime |
| Linux | GTK 3 and WebKitGTK 4.1 (Arch: `gtk3`, `webkit2gtk-4.1`) |
| macOS | System WKWebView |

Linux desktop mode uses Wails Purego. Building does not need GTK/WebKitGTK headers; service and CLI modes do not require the graphical libraries. The Linux executable still needs the system C runtime. Linux uses the desktop environment's StatusNotifierItem host and shows the window if no tray is usable.

## Window and service lifetime

Open `vista` without a command to open or restore the window and tray. `vista web` opens the default browser without a native window or tray. Both reuse the registered local service; new local launches bind to loopback by default, and reuse never changes an existing listener. `vista serve` runs in the foreground without opening a client, and `vista status` only reads the service state. See [the server entry points](../README.md#getting-started).

- Closing the window hides it when the tray is usable. **Quit desktop** removes the icon and leaves the service running. **Stop** stops the service and exits the desktop app.
- **Status**, **Restart** and **Stop** refer to the registered service. The tray requires confirmation for Restart and Stop and does not manage an instance built from another executable.
- **Launch at login** is off by default. When enabled, Vista starts in the tray if available, or shows the window otherwise. Disable it before moving or replacing the installed path and reenable it if needed.

Clicked external HTTP(S) links open in the system browser. The native window keeps the local service in its own WebView. Browser and WebView preferences remain separate. The **Host OS** in About describes the service's system, while **Runtime environment** describes the current client.

See [verification](./testing.md) for native archive tests and macOS signing checks.
