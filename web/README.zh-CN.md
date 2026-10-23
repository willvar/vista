# 前端开发

[English](./README.md)

## 入口

使用隔离的 Vista 后端。在 `web/` 安装锁定依赖，指定其回环地址并启动 Vite：

```sh
npm ci
VISTA_DEV_BACKEND=http://127.0.0.1:5186 npm run dev
```

端口使用后端实际值。开发服务监听 `127.0.0.1`，`/api` 经代理流式转发，仅改写开发页面自身的 Origin。未设置 `VISTA_DEV_BACKEND` 时 API 返回 503。浏览器只调用 Vista API，不直接请求上游模型。

| 命令 | 用途 |
| --- | --- |
| `npm run test:unit` | Node test runner 检查客户端、状态、composable 及国际化 |
| `npm run typecheck`／`npm run check` | Vue／TypeScript 检查，包含 Vite 和 E2E 配置 |
| `npm run generate:i18n` | 生成中文词条和后端回复语言名 |
| `npm run check:i18n` | 检查生成结果、词条键、占位符及消息语法 |
| `npm run build` | 生成／检查译文，构建内嵌 `app.js`／`vista-web.css` |
| `npm run test:e2e` | Playwright 项目，需要 `VISTA_CHROME` |

仓库根目录的 `make test frontend` 执行单测／类型／构建，`make test e2e` 构建独立应用并执行浏览器检查。一般要求和证据边界见[验证](../docs/testing.zh-CN.md)。

## 状态与界面边界

| 层次 | 入口 |
| --- | --- |
| HTTP／SSE 客户端 | `src/client/types.ts`、`vista.ts`、`sse.ts` |
| 事件状态 | `src/state/conversation.ts`、`workspace.ts`、`runTrace.ts` |
| 会话加载／发送 | `src/composables/useConversation.ts` |
| 共享工作区、模型、轮询 | `src/composables/useWorkspace.ts` |
| 服务商设置／历史执行 | `src/composables/useProviderSettings.ts`、`useRunHistory.ts` |
| 欢迎页／会话共用输入 | `src/ConversationSender.vue` |
| 上下文估算／压缩 | `src/ContextUsage.vue`、`ContextStatus.vue` |
| 路由及弹框布局 | `src/pages/`、`src/WorkspaceModal.vue`、`src/App.vue` |

设置和管理器通过命名路由视图覆盖会话，打开时 SSE 继续工作。手机设置先显示分类目录，再进入分类页；返回按此层级逐级退出，桌面保留设置侧栏。

切换会话中止旧请求，并用代次拒绝迟到回调。事件序号用于 SSE 重放去重及轮询快照合并；过时轮询不能覆盖更新的标题、执行和待答复状态。

主模型正文作为消息区独立行，与执行过程分离。`main-outputs` 提供历史索引，可见行加载全文；正式答复通过 `outputTurn` 接替对应行，不重复展示。推理按顺序放在执行过程中，无二次折叠；工具和子任务留在各自步骤。只有调用次数或整轮失败不产生空过程面板，整轮诊断在消息区展示。

消息行和长过程使用虚拟渲染及实际高度测量。离开窗口的全文缓存回收，返回历史时重新加载。`useFullOutput` 共享缓存最多 32 项工具输出、约 8 MiB 文本；缓存边界不截断当前展开内容。

稳定选中文字 350 ms 后提供引用追问，拖动／选区变化、复制、滚动或 Escape 收起。引用草稿按会话保存，发送时作为 Markdown 与问题共用普通输入链路，包括插话和排队。合并后的消息仍受 8 KiB 输入上限约束，失败保留完整草稿。编辑和撤回共用[会话回退约定](../docs/message-revert.zh-CN.md)。

`src/ui/appearance.ts` 保存 `vista-appearance` 并同步同源标签页；语义颜色位于 `src/ui/tokens.css`，组件主题在 `src/ui/theme.ts`。启动时先于样式应用主题；动效跟随 `prefers-reduced-motion`，包括组件动效、CSS 过渡／动画及 JavaScript 平滑滚动。

## 国际化

语言注册与客户端匹配位于 `src/i18n/languages.ts`，地区选项位于 `src/i18n/regions.ts`。只列已维护的文字变体，不因 CLDR 地区或日期格式差异扩充列表。自动模式及单一地区语言隐藏地区控件。生效地区用于格式化及下一执行的默认回复语言，用户明确语言要求优先。

简体中文维护 `src/i18n/locales/zh-CN.ts`，校订位于 `src/i18n/chinese-overrides.ts`。`scripts/generate-chinese.ts` 使用锁定的 OpenCC JS 生成台湾（`twp`）、香港／澳门（`hk`）、新加坡／马来西亚词条，不直接编辑生成文件。生成保留占位符、行内代码及 URL；OpenCC 仅用于构建，不进入浏览器运行代码。

其他地区文案差异在 `src/i18n/regional-overrides.ts`，新增语言需登记组件库 locale。`scripts/generate-reply-languages.ts` 从同一注册表生成 `internal/replylanguage/language_names.json`。开发和生产构建均执行这两类生成，`check:i18n` 拒绝过期结果。测试覆盖键、语法、占位符、地区匹配／格式、后端语言名及复数。

## 浏览器检查

[playwright.config.ts](./playwright.config.ts)定义：

| 项目 | 视口 |
| --- | --- |
| `desktop` | 1440 × 900 |
| `mobile-portrait` | 390 × 844，触摸 |
| `mobile-landscape` | 568 × 320，触摸 |

`VISTA_CHROME` 指定浏览器；直接运行 Playwright 时可设置 `VISTA_BIN`，未指定则 fixture 使用仓库 `bin/vista`。优先通过 `make test e2e` 构建隔离的 `.vista/test-state/bin/vista-e2e`。查看用例列表：

```sh
VISTA_CHROME=/absolute/path/to/chrome npm run test:e2e -- --list
```

[e2e/fixtures.ts](./e2e/fixtures.ts)管理独立配置／状态、明确端口、模拟模型及清理。报告、失败截图、trace 和浏览器 profile 留在 `web/.vista/test-state/`，失败不自动重试。目录测试预设自动检查时间，避免意外访问上游。
