# Wails desktop window

[简体中文](./desktop.zh-CN.md)

`vista-desktop` opens the WebUI served by Vista in a native window. The application uses the same pages, HTTP endpoints, and event streams as the browser. The desktop process does not host a second frontend or expose Vista Go methods to the page. System integration remains in the desktop process.

## Build and run

The desktop build uses Python 3. On Linux, also install a C compiler, `pkg-config`, and development libraries for GTK 3 and WebKitGTK 4.1. For example, Arch uses `gtk3` and `webkit2gtk-4.1`; Debian and Ubuntu use `libgtk-3-dev` and `libwebkit2gtk-4.1-dev`. The built desktop executable needs the matching runtime libraries, not the development headers. The Vista server does not depend on these GUI libraries.

```sh
make desktop
./bin/vista-desktop
```

On Windows, run `make desktop` and open `bin/vista-desktop.exe` with the WebView2 Runtime installed.

Keep the server executable beside the desktop executable. The window invokes `vista start` without opening a browser; a new companion binds to loopback and an existing listener is not rebound. Closing a window does not stop the server. On Windows, closing the main window while the tray is available hides it; the tray opens it again. When an older or different service is already running, the desktop can open its WebUI but does not register another tray or stop the existing service. Quit desktop only removes the desktop process and icon. Stop shuts down both the selected local service and the desktop app. To attach to an explicitly selected running service without starting one:

```sh
./bin/vista-desktop --server http://127.0.0.1:5186
```

The window loads the selected service directly. External HTTP(S) links opened by the user go to the system browser. Conversations, service settings, and saved composer drafts belong to the selected Vista service and Profile. Browser and WebView preferences remain separate; unacknowledged edits stay in the tab until saved. See [verification](./testing.md) for testing the shared WebUI.

About Vista lists the desktop client and its rendering engine under Runtime environment. Host OS describes the connected service, not the machine rendering the page. If this desktop window has a registered tray that can restart the connected local service, About leaves that action in the tray. A browser or a window without that control retains the Restart action.

With appearance set to System, WebKitGTK passes the desktop's light or dark preference to the WebUI. Changes apply without reloading the page. An explicit Light or Dark choice still takes precedence.

`make build` builds the standalone Go service. Windows `make package` includes both executables; the Linux package remains a standalone service. `desktop/build.py` applies a pinned Windows patch for WebView2 navigation enforcement, disabling the page message channel, and reporting tray registration. It does not modify the shared module cache.
