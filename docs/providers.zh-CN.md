# 模型连接、目录与路由

[English](./providers.md)

## 连接与发现

WebUI 连接列表来自[服务商配置](../internal/modelcatalog/provider_setup.go)，内置入口要求有可用模型资料。常见 API Key 服务商连接后加载目录，不要求用户在表单中选择模型 ID。自定义、Zen、Copilot 和本地连接发现各自可用模型；目录条目不代表账号具有使用权限。

连接有独立 ID。即使地址或模型 ID 相同，凭据、发现、编辑、默认值和断开操作仍按连接隔离。读接口只报告凭据是否存在，不回显密钥。

自定义连接选择一种对话 API 格式，连接内所有模型使用该格式：

| 格式 | 实现 |
| --- | --- |
| OpenAI Chat Completions | [openai_chat.go](../internal/provider/openai_chat.go) |
| OpenAI Responses | [openai_responses.go](../internal/provider/openai_responses.go) |
| Anthropic Messages | [anthropic.go](../internal/provider/anthropic.go) |
| Google Gemini generateContent | [gemini.go](../internal/provider/gemini.go) |
| Cohere Chat v2 | [cohere.go](../internal/provider/cohere.go) |

认证独立选择 Bearer、`x-api-key`、`x-goog-api-key` 或无认证，更换格式不修改认证方式。自定义请求头覆盖同名默认头，用于发现、对话、标题和压缩；目前仅自定义连接支持任意请求头。目标地址不变时，编辑中的空值保留已存密钥头值，移除字段即删除该请求头。

元数据补充名称、能力、限制和推理档位，不覆盖自定义连接明确选择的格式。失败时不探测或切换协议。手动模型 ID 是自定义发现失败或返回空列表后才显示的兜底，保留具体发现错误。

发现周期分别实现：自定义／Zen 每分钟、Copilot 每五分钟、本地服务每 30 秒。临时错误保留上次成功清单；编辑连接后，过时发现响应失效。见[自定义发现](../internal/server/custom_models.go)、[Copilot 发现](../internal/server/copilot_models.go)及[本地发现](../internal/server/local_models.go)。

## HTTP 客户端身份

关于 Vista 在模型目录下方提供通用请求的 `User-Agent` 设置，可选 Vista、Codex、OpenCode、OhMyPi、Claude Code、Gemini CLI、Kimi Code、Cline、GitHub Copilot 或自定义。选择后先预览通用请求的完整默认请求头，保存后生效。默认格式为 `vista/<版本> (<操作系统>; <架构>)`，例如 `vista/0.1.0 (Linux; x86_64)`；开发构建使用 `dev`。其它预设采用随 Vista 提供的兼容字符串，不实时探测客户端版本。Gemini CLI 预设仅包含产品名和版本前缀，不附带模型相关信息。自定义值允许单行、最多 512 个可打印 ASCII 字符。

[配置和 HTTP 公用实现](../internal/useragent/useragent.go)在每次新的通用请求时读取配置目录下的 `user-agent.json`，常驻 Worker 也会读取最新设置。可配置的默认值用于普通模型 API、模型发现、目录下载、网络搜索／网页读取及远程 MCP 工具请求；连接中显式配置的请求头优先。网页读取遵循所选 UA，失败后尝试排序中的下一个读取服务。

登录、令牌交换及特殊渠道的身份不受此设置影响。Codex 授权、刷新及账号模型请求共用随 Vista 提供的 `codex_cli_rs` 标识和版本；Copilot 设备授权使用其 OAuth 标识，账号查询、模型发现及请求使用随 Vista 提供的 Copilot CLI 标识及配套的编辑器／集成请求头。AWS Bedrock 保留 SDK UA，除非连接明确覆盖；Vertex 凭据及实例元数据、GitLab／SAP／IBM 令牌交换和 MCP OAuth 保留各自 HTTP 客户端或 SDK 的行为。这些路径不读取通用 UA 设置，因此修改或损坏该配置文件不会替换其身份或阻断令牌交换。浏览器请求及独立启动的命令使用各自的 HTTP 客户端。兼容配置和模拟验证不代表已确认真实服务商接受请求或允许相应账号用法。

`GET /api/user-agent` 返回保存的选项、可用预设及解析后的请求头；`PUT` 校验后原子保存，不修改正在进行的请求。

正式版本构建（`make build VERSION=v0.1.0`）会先运行[版本同步脚本](../scripts/update_user_agents.mjs)，从八个客户端的官方 npm 包获取 `latest` 元数据，只接受稳定版本，并生成[内置版本常量](../internal/useragent/versions_generated.go)。获取失败、元数据无效、预发布版本或版本倒退时，保留整份现有快照并中止构建。脚本只更新版本号，不修改 UA 格式，也不安装或运行这些客户端。

`make update-ua` 可单独刷新快照；`node scripts/update_user_agents.mjs --check` 只检查上游版本，不写入文件。普通 `dev` 构建复用现有快照，不获取 UA 元数据。保存发布快照后，可用 `make build VERSION=v0.1.0 UPDATE_UA=0` 复用它进行可复现或多平台重建。生成文件不含时间戳，内容不变时不重写。

## 内置集成

内置入口封装平台特有的端点和认证要求：

- OpenAI API Key 与 OpenAI Codex 是独立入口。Codex 使用 ChatGPT 设备码登录，拥有独立的 OMP 模型清单及凭据续期。
- Copilot 提供设备码登录和账号模型发现。
- Azure 区分实际模型身份与请求使用的部署名。
- Bedrock 按凭据来源使用 AWS 签名／Converse 或 Mantle Responses。
- Vertex 使用统一入口，按模型选择 Gemini、Anthropic 或 MaaS；API Key 模式只列出兼容 Gemini 模型。
- Ollama、LM Studio、vLLM 在本地发现模型；服务本身无需认证时可直接连接。

其他原生传输还包括 Mistral 和 Vercel AI Gateway。它们属于内置集成细节，不是自定义连接选择器中的额外格式。共享[凭据解析器](../internal/provider/credential_refresh.go)通过 Server 获取可续期请求凭据，按凭据来源合并交换；显式密钥和短期 token 仍由用户维护。

## 目录归属

| 来源 | 用途 |
| --- | --- |
| [内置 OMP 快照](../internal/provider/omp_models.json) | 离线模型身份、能力、限制、API 元数据和 thinking 策略 |
| models.dev 在线目录 | 补充、更新模型资料，合并前独立保存 |
| [服务商路由](../internal/provider/catalog_providers.csv) | Vista 协议包、端点及凭据环境变量 |
| [路由覆盖](../internal/provider/catalog_overrides.csv) | 必需的模型级协议或端点差异 |
| [生成的回归基准](../scripts/provider_matrix/testdata/catalog.csv) | 确定性的路由／配置对照，不是真实请求结果 |

[快照生成器](../scripts/build_omp_model_catalog.py)接收明确的 OMP checkout，普通构建不通过它联网抓取。运行时检查整体更新 OMP 和 models.dev。合并按精确服务商／模型 ID 匹配，不通过去掉 free 后缀或近似型号复制能力。OMP 保留 API 和特殊 thinking 策略，models.dev 可补充名称、输入能力、限制及显式推理选项；其端点和认证不进入 Vista 路由。

Zen、自定义、Copilot 和本地发现仍决定账号／服务可用性。元数据本身不能证明特定中转的工具、图片、推理或上下文容量，超限处理[独立于目录限制](./context-compaction.zh-CN.md)。

## 下载与安装

[OMP 下载](../internal/provider/omp_catalog_update.go)先通过 GitHub Contents API 获取文件 SHA，再随机尝试 GitHub Raw 和 jsDelivr，字节必须匹配 Git blob SHA。同版本跳过 OMP 文件下载，但仍检查 models.dev。

[models.dev 下载](../internal/provider/models_dev_catalog.go)使用 models.dev、models.opencode.ai 和 Stencil。地址随机排序，每个最多尝试一次，每次下载期限为 15 秒。对有效元数据规范化计算摘要，忽略格式和无关镜像字段；已替代摘要保留三天以拒绝已知旧内容。这是镜像延迟启发式，不是未知摘要较新的证明。

普通新增和小修正可由单一发布方提供。[风险变化](../internal/provider/models_dev_reliability.go)包括删除、API 变化、能力／档位丢失、已知限制大变及大范围修改，需要另一发布方确认受影响服务商／模型的具体字段。models.dev 和 models.opencode.ai 算同一发布方，Stencil 为另一方；无关新增不必一致。未确认变化保留现用目录并列出冲突，镜像返回现用内容不抵消已知分歧。ETag 和旧摘要历史随目录保存。

两份来源先暂存、校验，再原子保存到配置根目录的 `omp-models.json`。任一来源、连接校验或保存失败均保留现用安装。内容变化才推进更新时间，未变化检查不推进。重启重新加载原始来源并重建合并；新执行无需重启服务即可使用更新。

[自动计划](../internal/server/model_catalog_updates.go)将结果、下次检查及七天历史独立保存在 `model-catalog-status.json`。成功后 24 小时加最多 15 分钟随机延迟，失败六小时后重试，启动时到期即检查；手动与自动检查互斥。`GET /api/model-catalog` 读取状态和历史，`POST` 发起检查。「关于 Vista」在历史弹框外显示内容更新时间，检查与计划详情放在弹框内。

## 协议与验证边界

适配器保留通用正文／工具配对，处理协议特有回放字段。服务商已执行的托管调用保存 opaque ID，不再作为本地工具执行。超过解析器来源数量上限的引用被忽略，不导致整流失败。HTTP／流处理共用 [model_http.go](../internal/provider/model_http.go) 的错误、取消及缓冲规则；明确超限进入压缩，普通失败不重放工具副作用。

修改内置目录或路由后重新生成确定性基准：

```sh
GOWORK=off go run ./scripts/provider_matrix > scripts/provider_matrix/testdata/catalog.csv
GOWORK=off go test ./internal/provider ./internal/modelcatalog ./scripts/provider_matrix
```

其中 `configuration_only`、`unverified_per_model`、`unverified` 表示证据边界，不是完成的验收清单。协议测试使用本地模拟端点，不代表每个账号／模型均已真实兼容。本地真实服务检查有独立[脚本](../scripts/live_local_chat_smoke.mjs)。可复用测试入口见[验证](./testing.zh-CN.md)。
