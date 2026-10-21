# Worker 与后台任务生命周期

[English](./process-lifecycle.md)

Linux 和 macOS 上每个会话 Worker 是独立进程会话的 leader，Shell／本地 MCP 各有进程组并保留 Worker 会话归属。正常退出时 Worker 清理本会话子进程；意外退出时 Server 清理并回收 Worker。Linux 使用 `/proc` 启动时间和 pidfd 核对身份，macOS 使用 `sysctl` 启动时间与会话 ID。Server 保存 Worker 的 PID 和启动身份，下次启动时核对身份并处理遗留进程。macOS 没有 Linux 的父进程死亡信号，Server 退出后由 IPC EOF 触发 Worker 收尾；再次启动也会核对持久化的 Worker 身份。

Windows 使用带 `KILL_ON_JOB_CLOSE` 的 Job Object。启动受管子进程前先为当前进程建立外层 Job，子进程在创建时自动继承；Worker、Shell、本地 MCP 另以挂起状态启动，归入各自 Job 后才继续执行，覆盖父进程在启动中途崩溃的窗口，也避免快速派生子进程逃过归属登记。Job 句柄不传给子进程；所属父进程退出或崩溃时，系统回收该 Job 内的进程树。显式停止同样终止整棵树。持久化身份使用 Windows 进程创建时间，防止 PID 复用误杀。CLI 启动 Server 时同时脱离控制台与允许脱离的终端／SSH Job，服务在启动连接关闭后继续运行；受管 Worker 和工具仍保留在 Vista 的 Job 内。

关闭 Vista 会话会停止 Worker，切换页面或关闭浏览器不会。

后台 shell job 和 `detached` 子任务都归属 Vista 会话，可以跨过一次前台执行的结束或取消。`required` 子任务阻塞父任务提交，`best_effort` 在父任务收尾时取消。显式停止子任务会取消其现有后代，包括 detached 后代；正常父任务收尾保留 detached 工作。

子任务启动先持久化并 ACK，再返回任务 ID 和执行模型调用；终态与报告也在确认落盘后才供父任务收集。后台任务及其后代在父执行结束后仍可向原执行过程写入模型正文、工具记录和报告；它们不生成新的正式 assistant 消息。状态保存在 `agent_tasks`，报告正文复用事件与模型输出分块。Worker 退出、会话关闭或 Vista 重启会把未完成任务记录为中断，不自动恢复副作用。

工具执行先持久化并 ACK 工具开始事件。未能确认结果的工具记录为 unknown；正在运行的后台子任务不使已经完成的前台执行变成失败。显式停止某个子任务不改变已提交的前台答复。

前台 shell 和大工具输出保存为 artifact；后台 shell 保存为 job。中断后保留已保存字节，并明确输出是否完整。Unix 进程清理覆盖仍属于 Worker 进程会话／工具进程组的后代，无法保证清理主动 `setsid` 脱离会话的外部程序。Windows Job 覆盖正常派生的后代；通过系统服务等外部机制创建的进程不在此保证范围内。

仓库根目录执行 `make test integration` 会覆盖普通 Worker 崩溃、Server 崩溃及 Server/Worker 双崩溃，包含在 `make test all` 中。底层 Linux 集成入口为 `scripts/worker_crash_smoke.mjs` 和 `scripts/background_job_smoke.mjs`；前者可设置 `VISTA_CRASH_MASTER=1`，后者可设置 `VISTA_DOUBLE_CRASH=1`。直接运行脚本时使用独立测试状态和 `VISTA_BIN` 指定待测产物。

跨平台底层测试位于 `internal/platform/platform_test.go`，Windows 另验证父进程突然退出会关闭 Job。`cmd/vista/platform_test.go` 的 `TestNativeApplication` 通过真实应用产物检验启停、配置、Worker、Shell、取消、后台任务与崩溃恢复；设置 `VISTA_TEST_BINARY` 后在目标平台运行。运行验证需要在目标操作系统实际执行，不能从交叉编译成功推导。
