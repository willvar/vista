# 原生窗口与托盘

[English](./desktop.md)

`vista` 可执行文件自带本机服务、WebUI、CLI，以及 Wails 原生窗口和托盘。窗口和浏览器共用配置、会话、HTTP 接口和事件流。网页不能调用 Vista 的原生 Go 方法。

## 构建与安装

`make build` 构建 `bin/vista`（Windows：`bin/vista.exe`）；`make package` 生成一个压缩包。两者都需要 Python 3。交付产物统一使用 `CGO_ENABLED=0`。macOS 包中的可执行文件位于 `Vista.app/Contents/MacOS/vista`。

| 平台 | 原生窗口需要的运行库 |
| --- | --- |
| Windows | WebView2 运行库 |
| Linux | GTK 3 和 WebKitGTK 4.1（Arch：`gtk3`、`webkit2gtk-4.1`） |
| macOS | 系统自带的 WKWebView |

Linux 桌面模式使用 Wails Purego。构建不需要 GTK/WebKitGTK 开发头文件，服务和 CLI 模式也不依赖图形库。Linux 可执行文件仍需要系统 C 运行库。托盘使用桌面环境的 StatusNotifierItem 面板；没有可用托盘时显示窗口。

## 窗口与服务生命周期

不带参数打开 `vista` 会打开或唤回窗口和托盘。`vista web` 打开默认浏览器，不开启原生窗口或托盘。二者复用已登记的本机服务；新启动的本机服务默认监听回环地址，复用不改变现有监听地址。`vista serve` 在前台运行，不打开界面；`vista status` 只查询状态。参见[入口说明](../README.zh-CN.md#开始使用)。

- 托盘可用时关闭窗口只隐藏。**退出桌面**移除图标而不中断服务；**停止**则停止服务并退出桌面。
- **状态**、**重启**和**停止**针对已登记的服务。托盘重启和停止需确认，也不会管理由另一个构建启动的实例。
- **登录后启动**默认关闭。开启后优先驻托盘，没有托盘时打开窗口。移动或替换安装路径前先关闭该选项，之后可按需重新启用。

用户点击的站外 HTTP(S) 链接交给系统浏览器，原生窗口的 WebView 仍只显示本机服务。浏览器和 WebView 各自保存本地偏好。「关于 Vista」的「宿主系统」显示运行服务的系统，「运行环境」显示当前客户端。

原生包测试和 macOS 签名验收见[验证](./testing.zh-CN.md)。
