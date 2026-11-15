# 提示词缓存

[English](./prompt-caching.md)

Vista 在真实模型请求中组织有利于缓存复用的输入。服务商负责存储、过期、适用条件和计费。前缀不变、显式断点或稳定缓存键都不能保证命中或降低成本。Vista 不发送后台预热、保活请求，也不自动创建、续期或删除缓存资源。

## 随请求应用的策略

[能力选择与断点编译](../internal/provider/prompt_cache.go) 参考 [OpenCode 的随请求缓存策略](https://github.com/anomalyco/opencode/blob/6dd40633a15ec82c5fa2f2c4ea17e22c2ba67084/packages/ai/src/cache-policy.ts)，按其 [MIT 许可证](../licenses/opencode/LICENSE) 改编。选择依据包括 API 格式、服务商、模型及显式兼容性覆盖。未知自定义服务不会自动收到厂商专用字段。

对于显式标记 API，Vista 选择最后一个工具定义、首尾系统内容块及最后一个可缓存的对话内容块。在一条消息内优先选择文本；只有工具结果的尾部也可以缓存。整个请求最多四个标记。已有手动标记占用预算并保留有效期；超额标记或不合法的有效期顺序会被拒绝，与手动有效期冲突的自动标记会被跳过。签名思考块完整保留且不添加缓存标记。这些变化只存在于请求副本中，不改写保存的历史。

| 通道 | 默认行为 | 显式选项与边界 |
| --- | --- | --- |
| 原生 OpenAI Chat / Responses | 服务端自动缓存前缀；在支持的通道发送会话派生的 `prompt_cache_key` | `retention` 传递 `in_memory` 或 `24h`；支持的新 Responses 模型通过 `ttl` 设置 `prompt_cache_options.ttl`（`30m` 或 `12h`）。不启用预热或 OpenAI 仅显式缓存模式。 |
| Anthropic / Vertex Messages | 工具、系统和对话边界添加 `cache_control` | 使用服务默认有效期，或指定 `5m` / `1h`；写入可能计费。系统内容块保留指令权限。 |
| Bedrock Converse | 支持的 Claude 模型添加独立的 `cachePoint` 内容块 | 检查模型及仅支持五分钟的例外，兼容区域前缀。旧模型即使请求 `1h` 也省略 TTL 字段。Nova 等模型保留服务自身的隐式行为；显式 `bedrock` 格式可为不透明推理配置 ID 开启断点。 |
| Gemini / Vertex generateContent | 隐式缓存；不添加内容内标记，不管理资源 | 可按下文规则引用已有 `cachedContent`，替换校验通过的前缀。 |
| DeepSeek 等自动缓存 Chat 服务 | 稳定前缀并解析返回用量 | 不添加 Anthropic 标记或虚构的 OpenAI 缓存键；兼容的自定义服务可显式声明能力。 |
| OpenRouter / Vercel AI Gateway | Claude 添加 Anthropic 提示；Qwen 只为系统与历史添加提示；其他模型系列沿用自动缓存 | 按真实协议生成字段：Chat 使用 `cache_control`，AI SDK v3 使用 `providerOptions.anthropic.cacheControl`（Claude）或 `providerOptions.alibaba.cacheControl`（Qwen）。不为 Gemini 追加尾部缓存提示。原生 Alibaba Qwen Chat 使用系统与历史提示，生命周期由服务管理。 |

模型和服务支持可能变化。当前限制和收费以 [OpenAI](https://developers.openai.com/api/docs/guides/prompt-caching)、[Anthropic](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)、[Bedrock](https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html) 和 [Gemini](https://ai.google.dev/gemini-api/docs/caching) 文档为准。协议测试验证请求约定，不能证明真实服务权限或节省效果。

## 连接覆盖

自定义连接在现有表单中只提供一次明确的缓存格式选择。未知自定义服务默认显示“不发送缓存提示”；选择 OpenAI 后默认发送缓存键，不再提供独立开关。API／配置中的高级缓存键覆盖仍受支持，普通编辑会保留；明确切换缓存格式时则重置为所选格式的默认行为。已有自动配置按实际行为显示，普通编辑保留已保存的策略。这些覆盖设置应用于该连接下的所有模型；原生服务商的选择仍结合 API、服务商和具体模型判断。自动发现的模型继承连接设置与所选代理，同时使用各自的能力元数据。普通编辑保留这些选择；更换地址或格式时清除，除非再次显式选择。没有独立缓存管理页面。

连接 API（`POST` / `PUT /api/provider-config`）与保存的模型配置支持 `promptCaching`，`GET /api/model-config` 返回该设置：

```json
{
  "promptCaching": { "format": "openai", "key": true, "retention": "24h" }
}
```

`format` 可选 `auto`（默认）、`none`、`openai`、`anthropic`、`alibaba`、`bedrock` 或 `gemini`，并校验其与 API 格式的关系。`none` 不发送 Vista 的缓存提示，不能关闭服务自身的自动缓存。省略 `key` 时使用服务默认值；`true` / `false` 显式控制缓存键能力，与 Codex 路由独立。保存的配置可通过 `promptCacheKey` 指定可打印的缓存键，否则使用逻辑会话；过长键使用确定性的 SHA-256 摘要。已有 xAI `promptCacheKey` 继续使用原生 `x-grok-conv-id` 行为。

可选 `ttl` 为 Messages、Converse 指定 `5m` / `1h`，或为支持的 Responses 通道指定 `30m` / `12h`。`retention` 与 `ttl` 不能同时设置。省略有效期则保持服务默认值；更长有效期可能改变写入收费。显式 OpenAI 格式为不透明自定义模型 ID 声明对应选项。Codex 路由及旧配置兼容规则见[服务商文档](./providers.zh-CN.md#codex-路由)。

### 引用已有 Gemini 内容

高级配置可在 `promptCaching.cachedContent` 设置 `name`、`sha256`、`messages`。资源必须已在相同端点、账户和模型下创建。`messages` 计算开头的 **Gemini 投影内容条目**，并非 Vista 原始消息数。`sha256` 是下述对象使用 Go `encoding/json` 紧凑编码后的十六进制 SHA-256；映射键排序，内容为真实投影值：

```json
{"contents": [], "systemInstruction": null, "tools": null}
```

将 `contents` 换成相应前缀，并包含完整的投影系统指令和工具；不存在的字段使用 `null`。只有摘要完全匹配，Vista 才会从请求中移除这些字段和前缀，发送 `cachedContent` 与剩余内容，且必须保留至少一条新内容。规则、工具或前缀变化会在发送 HTTP 前失败，不会静默复用旧指令。标题、子任务及摘要请求不会继承该引用。此绑定不验证远端资源内容、是否存在或是否过期，创建方须确保内容匹配；创建和续期仍在外部完成。

## 指令与历史接续

主智能体通过确认后的 `model_history` 前缀条目和不可变检查点保存初始指令基线及最新已接纳快照。这些纯元数据条目不是服务商消息。后续规则、偏好或环境变化在实际时间位置追加可信指令快照，不替换原始基线。语言变化只追加语言控制，不重述旧的全局规则或运行事实；当前清单快照作为普通运行数据传递，不进入高权限基线。模型或连接变化建立新系列；没有前缀元数据的旧历史在下一次执行建立基线，不改写旧记录。

前缀历史条目以已提交输入的序号作为索引，使同一次模型请求前排队输入带来的语言变化具有独立标识，同时保留重传条目的幂等性。

Responses 将初始指令放入 `instructions`，后续可信变化作为历史内 developer 消息按序传递。其他适配器保留各自的指令放置约定，不据此承诺各协议具有相同缓存行为。实际操作仍检查当前目录规则和权限，固定提示词字节不等于冻结执行权限。

异步完成结果作为普通 user 角色的上下文数据，带本地 `RuntimeData` 标记，不代表新用户意图或高权限指令。恢复时保留原文和顺序；旧的 system 角色运行事件只在请求投影中改为数据，不修改保存内容。图片沿用仅激活一次请求的策略。原生 assistant 条目和加密推理作为不透明回放状态保留，不为缓存重新生成或摘要。

待回答问题的原生调用与未来答案保持配对。空闲估算及维护会推迟变化的指令快照，直到真实输入补齐该配对。压缩可以摘要已完成运行数据及已被替代的指令快照，但保留最新指令快照、当前真实用户输入及必要规则／调用组。

压缩成功后会移除过时语言控制，只保留最新完整指令快照之后的最后一条语言控制。若完整快照更新，则由该快照提供语言默认值。清理也兼容旧版未标记的 Vista 语言控制；压缩失败或取消时保留原上下文。

## 用量与有界诊断

缓存读取和写入是可选遥测。字段缺失保持未知，明确返回零才是实测零。统一输入总量只包含一次缓存读写：OpenAI、Gemini、Mistral 的输入已包含缓存，Anthropic Messages 与 Bedrock Converse 则需要将未缓存输入与读写相加。AI SDK 网关优先使用总数，否则由完整分项计算；总数不完整时保持用量未知，不使生成失败。推理仍属于输出。旧记录不反推重建。
适配器与 Server 的 Worker 结果入口共用规范化用量校验：各计数均在零至十亿之间，缓存读不超过输入，已报告的缓存写不超过输入减去缓存读。非法 Worker 用量在持久化和通知读取方之前被拒绝；未报告的写入量仍保持未知。

开启调试后，各请求适配器共用 HTTP 记录中的 `cache` 观察：可用计数、已发送请求字节数、历史条目数、系统与工具摘要、可确定时的首个变化位置及结构变化原因。移动缓存标记不算语义前缀变化，工具参数和原生重放内容仍参与比较。

开启调试诊断时，驻留会话为最多 16 个路由／模型／账户身份分别保留基线，普通用户回合不会清空。关闭诊断时，不为缓存比较捕获、解析请求正文或计算指纹；未采集的请求会清空基线，重新开启后建立新序列。缺失遥测不覆盖最近的实测基线；首次测量前使用可用请求进行结构比较。Codex 令牌重置不清除这些基线，Worker 重启则重新开始。每次比较最多覆盖 512 个输入摘要和 16 MiB 的诊断请求预算；`unchecked-tail` 或 `unchecked-request` 表示无法确认完整相等。这些限制不裁剪请求、响应、工具或原生重放数据。请求发送完成后释放捕获的提示词字节，结构观察只保留摘要和计数，不保留正文或路由令牌。详细 HTTP 捕获具有独立的启用条件、预算及脱敏。

仅追加的结果说明客户端输入结构，不代表服务商分词、缓存可用性、有效期或计费优惠已经得到确认。
