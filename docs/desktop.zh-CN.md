# Wails 桌面应用

[English](./desktop.md)

`vista-desktop` 在原生窗口中打开本机服务的 WebUI。浏览器和桌面端使用同一套页面、HTTP 接口和事件流。桌面进程负责系统集成，不向网页暴露 Vista 的原生 Go 方法。

## 构建和安装

`make build` 构建没有图形依赖的独立 Go 服务。`make desktop` 还会构建桌面端，需要 Python 3。

| 平台 | 渲染引擎 | 桌面包 |
| --- | --- | --- |
| Windows | WebView2 运行库 | 从 Windows 发布包解压，在 `vista.exe` 同目录运行 `vista-desktop.exe`。 |
| Linux | GTK 3 与 WebKitGTK 4.1 | `make package PACKAGE_DESKTOP=1` 包含两个程序和 `share/` 下的应用入口资源。可以把资源安装到本地前缀，把两个程序放入该前缀下加入 `PATH` 的 `bin/`；也可直接在 `vista` 同目录运行 `vista-desktop`。 |
| macOS | WKWebView | 在装有 Xcode 命令行工具的 macOS 上，`make package PACKAGE_DESKTOP=1` 组装内含两个程序的 `Vista.app`。本地打包不为公开分发做签名或公证。 |

Linux 构建还需要 C 编译器、`pkg-config` 及 GTK 3 和 WebKitGTK 4.1 开发库（Arch：`gtk3`、`webkit2gtk-4.1`；Debian/Ubuntu：`libgtk-3-dev`、`libwebkit2gtk-4.1-dev`）。安装后的桌面端只需相应运行库，不需要开发头文件。Linux 托盘使用桌面环境的 StatusNotifierItem 面板；没有可用托盘时，窗口不会隐藏到托盘。

## 窗口、托盘和服务

桌面端通过 `vista start` 启动或复用本机服务，不打开浏览器。新服务监听回环地址；复用不改变已有服务的监听地址。`vista-desktop --server http://127.0.0.1:5186` 可以连接已运行的本机服务而不启动新服务。托盘只控制已登记且匹配的服务；打开其他构建的 WebUI 不等于接管该服务，也不会新增第二个托盘。

- **打开 Vista** 唤回已有主窗口。同一配置下再次启动桌面端不会另开主窗口。
- **状态**、**重启**和**停止**针对所连接的本机服务。重启和停止需确认。连接异常不会显示为服务已停止。
- 托盘可用时，关闭主窗口只会隐藏。**退出桌面**移除图标而不中断服务；**停止**则停止服务并退出桌面。
- **登录后启动**默认关闭，由桌面端管理，不属于 WebUI 或无人登录时的服务自启。托盘可用时只驻托盘，否则打开窗口。移动或删除应用前先关闭该选项；需要时从安装后的路径重新启用。

用户明确点击的站外 HTTP(S) 链接交给系统浏览器。桌面窗口保留相同的 WebUI 功能，包括同一服务端的会话、设置和已保存草稿。浏览器和 WebView 各自保存渲染器本地偏好。「关于 Vista」区分客户端运行环境和服务端操作系统。当前桌面连接没有可用托盘控制时，「关于 Vista」仍保留重启入口。

原生包的验证方式见[验证](./testing.zh-CN.md)。桌面打包不改变 Windows 的原生发布门槛或 Unix 独立服务的发布门槛。
