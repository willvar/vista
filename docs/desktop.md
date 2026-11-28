# Wails desktop app

[简体中文](./desktop.zh-CN.md)

`vista-desktop` opens the local service's WebUI in a native window. Browsers and the desktop app use the same pages, HTTP API, and event stream. The desktop process owns OS integration; the page cannot call Vista's native Go methods.

## Build and install

`make build` builds the graphics-free `bin/vista-server`; `make desktop` builds `bin/vista-desktop`, with the service included, and requires Python 3. Windows executables use the `.exe` suffix. `make package-server` and `make package-desktop` create separate named archives; neither contains the other program.

| Platform | Engine | Desktop use |
| --- | --- | --- |
| Windows | WebView2 Runtime | Extract and open `vista-desktop.exe`. No `vista-server.exe` or `vista.exe` is required. |
| Linux | GTK 3 and WebKitGTK 4.1 | Run `vista-desktop` directly, or install it and the app entry resources under `share/` into the same local prefix. |
| macOS | WKWebView | Open `Vista.app` from the archive. Local development packages are not signed or notarized; release candidates verify both. |

Linux builds also need a C compiler, `pkg-config`, and the GTK 3 and WebKitGTK 4.1 development libraries (Arch: `gtk3`, `webkit2gtk-4.1`; Debian/Ubuntu: `libgtk-3-dev`, `libwebkit2gtk-4.1-dev`). The installed desktop app needs the corresponding runtime libraries, not the development headers. Linux uses the desktop environment's StatusNotifierItem panel. Without a reachable tray host, the window does not hide to the tray.

## Window, tray and service

The desktop executable starts itself in background roles for the service, Workers and MCP hosts. Its window loads the same WebUI over local HTTP/SSE. A new service binds to loopback; reuse does not change an existing listener. `vista-desktop --server http://127.0.0.1:5186` attaches without starting a service. The tray only controls the running instance registered for this executable; attaching to a separate Server or another build does not take it over.

- **Open Vista** shows the existing main window. Launching the desktop app again does not create another main window for the same configuration.
- **Status**, **Restart** and **Stop** refer to the connected local service. Restart and Stop require confirmation. A connection error is not reported as a stopped service.
- Closing the main window hides it when the tray is usable. **Quit desktop** removes the icon and leaves the service running. **Stop** stops the service and exits the desktop app.
- **Launch at login** is opt-in and belongs to the desktop process, not the WebUI or an unattended service. It starts in the tray when possible; otherwise the window opens. Disable it before moving or removing the app, and enable it again from the installed path if desired.

Explicitly clicked external HTTP(S) links open in the system browser. The desktop window retains the same WebUI functionality, including the same service-side conversations, settings and saved drafts. Browser and WebView preferences that are local to the renderer remain separate. About Vista distinguishes the client runtime from the service's host OS. Restart remains in About when the current desktop connection does not have a usable tray control.

See [verification](./testing.md) for native archive tests. Desktop packaging does not change the Windows native gate or the standalone Unix release gate.
