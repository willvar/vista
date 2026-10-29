# 会话资源

[English](./resources.md)

Vista 将原始附件存为 `state/resources/<哈希前两位>/<完整 SHA-256>`，相同字节跨会话共用文件，不缩放或重新压缩。前端与 Worker 对 PNG／JPEG／WebP／GIF 原件统一限制为每张 50 MiB。`read_resource` 也能导入本地图片，包括工具下载的图片及已提取的视频帧；不支持音视频输入或自动提帧。资源库不包含项目工作目录、工具产物或进程日志。

## 引用与保留

SQLite 保存 `resources` 元数据、`session_resources` 所有权和有序的 `message_resources`／`queued_input_resources` 关联。原始文件名属于各引用，共享字节不会覆盖另一会话的文件名。图片二进制不存入 SQLite、Worker 命令或上下文检查点。

会话是保留根。关闭会话、撤掉或丢弃排队输入、压缩、取消、失败及重启保留所有权。删除会话级联删除关联，其他会话持有的共享原件保留。消息回退是另一例外：仅被撤销历史持有的关联会释放，不在该会话资源列表中继续暴露。物理原件仍在磁盘，直到用户在存储空间手动清理完全无引用文件。没有自动回收或保留聊天同时剥离附件的操作。

更早的工具导入及补读记录在后续回退中保留对应所有权；编辑消息时，只额外保留本次明确提交且已属于该会话的图片引用。

新附件从引用加载到下一请求的临时副本，受当前模型视觉输入能力约束。成功后，后续请求只保留元数据和哈希；模型需要再次观察时显式调用 `read_resource`。纯文本模型看到元数据和省略说明，切换模型不自动重发历史附件或改写规范历史。

`read_resource` 必须且只能提供 `path`（本地图片绝对路径）或 `hash`（该会话持有的完整哈希）之一。按路径读取先复制原件到资源库再激活，源文件变化或删除不影响后续读取。

摘要保留资源哈希。`list_resources` 使用哈希游标列出保留原件，`read_resource` 将获准原件作为下一次请求的视觉输入。即使摘要遗漏哈希，也可通过列表找回图片。运行时图片内容投影在完整工具结果组之后，不持久化伪造用户消息；单次请求激活状态不写入历史或检查点。当前请求撞墙恢复时保留待发送附件，成功前不会失去激活，也不会重新激活历史图片。子任务可用会话资源工具，但不自动继承父任务当前图片载荷。

## 磁盘操作与 API

发布时流式写临时文件、计算 SHA-256、sync 后重命名。跨进程锁覆盖发布至引用提交、读取，以及清理的最终引用复查和删除。事务失败可留下可清理文件，不留下指向半成品的已提交引用；读取校验大小及完整哈希。Server 使用固定大小缓冲区将原始上传流写入 `state/inputs/` 暂存文件。会话 Worker 校验并导入原件，等待 Server 确认所有权已提交后才返回引用。操作结束或中断后下次启动时删除暂存文件；已发布但未建立所有权的原件仍由用户手动清理。

只有 Worker 会加载图片字节、组装含图片的模型请求，包括自动标题。Server 下载接口流式校验大小和哈希再返回原件；草稿与回退接口只返回引用。

浏览器先下载已发送消息的原图，再执行撤回，避免释放历史关联后无法将附件放回草稿。

- `POST /api/sessions/:id/resources?name=<文件名>` 接收原始图片请求体及图片 `Content-Type`，最多 50 MiB，要求会话已打开且 Worker 就绪；所有权提交后返回 `{hash, name, mediaType, bytes}`。
- 消息提交的 `images` 携带这些引用，消息 API 不接受 Base64 图片请求体。

- `GET /api/resource-storage` 返回异步操作和最近扫描；`POST` 的 `{"action":"scan"}` 或 `{"action":"clean"}` 返回 202 并发起操作，并发请求共用已有操作。
- 占用按实际文件去重，区分被引用及可清理数量／字节。清理在每次删除前复查所有权，报告数量和释放空间，结束后重新扫描。关闭设置不取消服务端操作，组件卸载会中止页面请求。
- `GET /api/sessions/:id/resources/:hash` 只向持有原件的会话提供内容，消息图片 URL 按有序附件读取。

## 验证入口

[资源库测试](../internal/resources/library_test.go)、[存储测试](../internal/store/resource_storage_test.go)、[上下文测试](../internal/agent/resource_context_test.go)、[协议图片测试](../internal/provider/media_test.go)、[存储 API 测试](../internal/server/resource_storage_test.go)及[Worker 上传测试](../internal/server/resource_upload_test.go)覆盖完整性、独立归属／文件名、关闭／重启／删除、失败提交、并发清理、选模变化、检查点及工具补读。

前端测试覆盖跨会话迟到的撤回读取；[存储空间 E2E](../web/e2e/resource-storage.spec.ts)和[图片 E2E](../web/e2e/images.spec.ts)使用隔离服务、模拟模型及 Chrome profile 检查界面与持久化。
