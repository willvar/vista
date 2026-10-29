# Worker 与后台任务生命周期

[English](./process-lifecycle.md)

MCP 连接由主进程监督的独立托管进程统一管理，本地 MCP 子进程归属于托管进程的进程会话或 Windows Job，而不归属于某个聊天会话。会话结束释放连接版本的占用；关闭浏览器不影响连接。托管进程退出时清理其本地进程树；意外退出后由主进程退避重启，但不重放已中断的工具调用。`state/mcp-host.json` 保存进程身份，供下次启动或重置清理；`state/mcp-host-data/` 中的临时结果及授权快照随占用释放或恢复清理。托管进程的实测内存计入总预算，并使用与会话进程相同的单进程软上限。

Linux 和 macOS 上每个会话 Worker 是独立进程会话的 leader，Shell 拥有独立进程组并保留 Worker 会话归属，本地 MCP 则归属于托管进程的进程会话。正常退出时 Worker 清理本会话子进程；意外退出时 Server 清理并回收 Worker。Linux 使用 `/proc` 启动时间和 pidfd 核对身份，macOS 使用 `sysctl` 启动时间与会话 ID。Server 保存 Worker 的 PID 和启动身份，下次启动时核对身份并处理遗留进程。macOS 没有 Linux 的父进程死亡信号，Server 退出后由 IPC EOF 触发 Worker 收尾；再次启动也会核对持久化的 Worker 身份。

Windows 使用带 `KILL_ON_JOB_CLOSE` 的 Job Object。启动受管子进程前先为当前进程建立外层 Job，子进程在创建时自动继承；Worker、MCP 托管进程、Shell、本地 MCP 另以挂起状态启动，归入各自 Job 后才继续执行，覆盖父进程在启动中途崩溃的窗口，也避免快速派生子进程逃过归属登记。Job 句柄不传给子进程；所属父进程退出或崩溃时，系统回收该 Job 内的进程树。显式停止同样终止整棵树。持久化身份使用 Windows 进程创建时间，防止 PID 复用误杀。CLI 启动 Server 时同时脱离控制台与允许脱离的终端／SSH Job，服务在启动连接关闭后继续运行；受管 Worker 和工具仍保留在 Vista 的 Job 内。

Windows 可在资源管理器中双击 `vista.exe`，启动或复用已运行的实例，并打开默认浏览器。只隐藏资源管理器启动的启动器控制台，终端命令保留正常输出。启动失败时，原生对话框显示原因及日志路径；浏览器命令无法启动时，对话框提供已运行实例的访问地址。

Windows Server 在发布实例记录后，通过 `server.Run` 的就绪回调启动一个托盘图标和原生 UI 线程。再次打开不会增加图标。单击与双击合并为一次 `start` 操作；右键菜单提供 `restart`、解析后的原生状态窗口和 `stop`。状态窗口限时读取 `/api/status`，使用可滚动、可选择的只读文本控件，不阻塞托盘消息循环；关闭状态窗口不会停止服务。服务停止时关闭状态窗口并移除图标，隐藏的顶层托盘窗口通过 `TaskbarCreated` 在资源管理器重启后恢复图标。没有资源管理器的非交互 Windows 会话仍可提供服务，但没有可见图标。

托盘的生命周期命令以显式脱离的独立进程执行，因此 Restart 可在旧 Server 退出后完成。Server 的外层监管 Job 允许显式脱离；受管子进程各自的 Job 仍禁止脱离，且在恢复执行前完成归入。普通子进程仍在创建时继承外层 Job。隐藏的托盘操作失败时保留原生错误提示，普通终端命令继续使用原有输出。

关闭 Vista 会话会停止 Worker，切换页面或关闭浏览器不会。

确认全部重置后，会在 Server 进程内重启应用运行时。`internal/server/reset.go` 拦截新的 API 操作，取消旧运行时上下文，等待请求及 `App.background` 工作结束，停止 Worker 并核对遗留进程归属，再关闭 SQLite 并删除用户数据。所有可能写入运行文件、凭据或持久化状态的异步操作，都必须归入 `App.background`，或由已跟踪的调用者同步等待。实例锁始终保留。重新打开空数据后，如果重置后的端口发生变化，`runtimeListener` 会更换监听套接字；旧连接上的重置请求保留到返回新地址。失败时其他 API 保持不可用，但仍可重试重置或停止服务。

后台 shell job 和 `detached` 子任务都归属 Vista 会话，可以跨过一次前台执行的结束或取消。`required` 子任务阻塞父任务提交，`best_effort` 在父任务收尾时取消。显式停止子任务会取消其现有后代，包括 detached 后代；正常父任务收尾保留 detached 工作。

子任务启动先持久化并 ACK，再返回任务 ID 和执行模型调用；终态与报告也在确认落盘后才供父任务收集。后台任务及其后代在父执行结束后仍可向原执行过程写入模型正文、工具记录和报告；它们不生成新的正式 assistant 消息。状态保存在 `agent_tasks`，报告正文复用事件与模型输出分块。Worker 退出、会话关闭或 Vista 重启会把未完成任务记录为中断，不自动恢复副作用。

工具执行先持久化并 ACK 工具开始事件。未能确认结果的工具记录为 unknown；正在运行的后台子任务不使已经完成的前台执行变成失败。显式停止某个子任务不改变已提交的前台答复。

前台 shell 和大工具输出保存为 artifact；后台 shell 保存为 job。中断后保留已保存字节，并明确输出是否完整。Unix 进程清理覆盖仍属于 Worker 进程会话／工具进程组的后代，无法保证清理主动 `setsid` 脱离会话的外部程序。Windows Job 覆盖正常派生的后代；通过系统服务等外部机制创建的进程不在此保证范围内。

仓库根目录执行 `make test integration` 会覆盖普通 Worker 崩溃、Server 崩溃及 Server/Worker 双崩溃，包含在 `make test all` 中。底层 Linux 集成入口为 `scripts/worker_crash_smoke.mjs` 和 `scripts/background_job_smoke.mjs`；前者可设置 `VISTA_CRASH_MASTER=1`，后者可设置 `VISTA_DOUBLE_CRASH=1`。直接运行脚本时使用独立测试状态和 `VISTA_BIN` 指定待测产物。

跨平台底层测试位于 `internal/platform/platform_test.go`，Windows 另验证父进程突然退出会关闭受管 Job，而显式脱离的控制进程仍能存活。`cmd/vista/platform_test.go` 的 `TestNativeApplication` 通过真实应用产物检验启停、配置、Worker、Shell、取消、后台任务与崩溃恢复；`TestNativeResetPort` 验证重置、端口切换、实例发现和端口冲突后的重试；`TestNativeWindowsDesktopLauncher` 使用独立的父进程和浏览器替身，验证资源管理器启动路径、实例复用及原生启动错误对话框；`TestNativeWindowsTray` 验证图标注册、点击通知、右键菜单关闭、状态解析、图标恢复、存在 Worker 时重启及停止；`TestNativeWindowsTrayLongStatus` 验证长 Unicode 状态、滚动，以及状态窗口打开时的退出。设置 `VISTA_TEST_BINARY` 后在目标平台运行；图标检查需要运行资源管理器的交互式 Windows 会话。
