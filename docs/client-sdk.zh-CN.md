# Vista 客户端 SDK

[English](./client-sdk.md)

`@vista/client` 是 Vista WebUI 使用的同仓 TypeScript 客户端。它提供 ESM 和类型声明，不依赖其他运行时包。运行环境须提供 Fetch、Web Streams 和 `AbortSignal.any`，例如当前浏览器或 Node.js 24。这个私有包与 Vista 后端使用同一源码版本构建；目前不通过注册表发布，也不保证独立升级后端后的兼容性。

## 构建与接入

在仓库根目录生成可本地安装的包：

```sh
npm --prefix sdk ci
mkdir -p .vista/dist
npm pack ./sdk --pack-destination .vista/dist
```

在另一个 npm 项目用 `npm install /path/to/vista-client-0.0.0.tgz` 安装，不需要复制 WebUI 源码。内置 WebUI 则通过 `file:../sdk` 依赖同一个编译入口。

```ts
import { createVistaClient, type Session } from '@vista/client'

const client = createVistaClient({ baseURL: 'http://127.0.0.1:5186' })
const openSessions = await client.request<Session[]>('/sessions?open=true')
```

`baseURL` 是不含 `/api` 的服务地址，可以包含部署路径前缀；省略时使用同源的 `/api`。HTTP 读写、`image()` 图片读取和两条 SSE 都使用配置的 `fetch`、`headers`、`language` 和 `credentials`。`url()` 为浏览器原生媒体或预览解析 API 地址；浏览器元素不会附带 SDK 自定义的请求头，需要这些请求头时用 `image()` 读取图片。SDK 不授予后端访问权限，也不改变 [Vista 的部署边界](./product.zh-CN.md#当前边界)。

## 同步

- 每个工作区使用一个 `createWorkspaceSync({ client, status, metrics })`。订阅应用需要的主题，再调用 `start()`。同步器管理共享 SSE 和兴趣列表，按修订号和服务端代次交付数据。视图关闭即退订，所有者销毁时调用 `stop()`。需要补读快照时，订阅回调可以传入收到的取消信号。
- 当前会话使用 `createConversationSync`。在回调中接入 `restoreConversationSnapshot` 和 `reduceConversationEvent`（或等价的状态存储），提供已应用的事件／回复游标与权威队列状态。`start()` 读取有界快照；重连时先补读缺失记录，再接续持久化事件流。离开视图时取消传入的信号。`web/src/composables/useConversation.ts` 展示了如何保留分页窗口与并发发送确认。
- 通过 `request` 显式执行命令；同步器只恢复数据和订阅，不重发消息。已读只在界面确认内容可见后上报。路由、本地草稿、浏览器前台／联网信号和渲染由应用负责。

HTTP/SSE 行为与恢复约束见[架构](./architecture.zh-CN.md)。
