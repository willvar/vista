# Wails 桌面窗口

[English](./desktop.md)

`vista-desktop` 在原生窗口中打开 Vista 服务提供的 WebUI，和浏览器使用同一套页面、HTTP 接口和事件流。桌面进程不另做一套前端，也不向网页暴露 Vista 的 Go 方法。系统集成留在桌面进程中。

## 构建和运行

桌面构建需要 Python 3。Linux 还需 C 编译器、`pkg-config` 及 GTK 3 和 WebKitGTK 4.1 的开发库。例如，Arch 使用 `gtk3` 和 `webkit2gtk-4.1`；Debian 和 Ubuntu 使用 `libgtk-3-dev` 和 `libwebkit2gtk-4.1-dev`。构建出的桌面程序只需对应运行库，不需要开发头文件。Vista 服务不依赖这些图形库。

```sh
make desktop
./bin/vista-desktop
```

在 Windows 上运行 `make desktop`，并在安装了 WebView2 运行库的环境中打开 `bin/vista-desktop.exe`。

将服务和桌面可执行文件放在同一目录。窗口通过 `vista start` 启动或复用服务，不会打开浏览器。新启动的服务监听回环地址，复用不改变已有监听地址。关闭窗口不停止服务。Windows 有可用托盘时，关闭主窗口只会隐藏窗口，可从托盘重新打开。旧版或不同构建的服务已经运行时，桌面仍可打开它的 WebUI，但不会创建第二个托盘，也不会停止现有服务。「退出桌面」只退出桌面进程并移除图标。「停止」会停止所连接的本地服务并退出桌面应用。若要连接明确指定的已运行服务，而不启动新服务：

```sh
./bin/vista-desktop --server http://127.0.0.1:5186
```

窗口直接加载所选服务。用户点击的外部 HTTP(S) 链接交给系统浏览器。浏览器本地偏好和草稿各自保存在浏览器或 WebView 配置中；会话和服务设置属于所选服务。共享 WebUI 的测试见[验证](./testing.zh-CN.md)。

「关于 Vista」的「运行环境」显示桌面客户端及其渲染引擎。「操作系统」描述连接的 Vista 服务，不是显示页面的设备。如果桌面窗口的托盘已注册且可以重启所连接的本地服务，「关于 Vista」不再重复提供重启。浏览器或没有该托盘控制的窗口仍保留重启。

外观设为「跟随系统」时，WebKitGTK 会把桌面的明暗偏好传给 WebUI，切换系统主题无需刷新页面。手动选择「浅色」或「深色」时，仍以手动选择为准。

`make build` 构建独立 Go 服务。Windows 的 `make package` 打包两个可执行文件；Linux 仍打包独立服务。`desktop/build.py` 在 Windows 使用固定版本的补丁来限制 WebView2 导航、禁用页面消息通道并报告托盘注册结果，不修改共享模块缓存。
