# Changelog

本项目的变更记录。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 `主.次.修订`：`internal/` 重构（不改变对外契约）走修订号，
新增对外能力走次版本号，破坏性变更走主版本号。

## [0.1.4] - 2026-11-14

本轮以 issues.md 的 14 条运行问题为输入，按 **安全 → 可用性 → 账号池 → 结构 → 协议 → 交付**
顺序落地，并完成模型清单与桌面端对齐、指纹实测。对外契约有破坏性变更（管理面、密钥接口），
按约定走次版本号。

### Security（破坏性）

- **凭据不再明文落盘**：
  - 管理密码改为 `pbkdf2-sha256$<iter>$<salt>$<hash>`（210k 轮，随机盐），
    旧状态文件的 `admin_password` 明文在 `Open` 时自动迁移并清空；
  - API 密钥只保存 SHA-256 指纹与展示片段（`sk-abc123…wxyz`），
    **完整密钥仅在 `POST /admin/api/keys` 时返回一次**；
  - 密钥的启停 / 删除改为按 `id` 定位（`?id=`，兼容旧的 `?key=`）。
- **管理面不再有「未设密码则本机可裸奔」的退路**：未设置管理密码时
  `/admin/api/*` 一律返回 503。管理密码也不再能调用 `/v1/*`。
- **来源判定重写**（`internal/server/trust.go`）：`RemoteAddr` 之外还要看代理头。
  带 `X-Forwarded-For` / `X-Real-IP` / `Forwarded` 且来源不在
  `trusted_proxies` 时，一律按远程处理——堵住 Funnel / nginx 把来源变成
  `127.0.0.1` 导致的「仅本机」误放行。
- **管理鉴权爆破防护**：同一来源 5 分钟失败 5 次 → 429 + `Retry-After`；
  可选 `rate_limit_per_key` 对 `/v1/*` 按密钥限流（默认 0 = 不限）。
- 新增设置：`trusted_proxies`、`admin_loopback_only`（裸机默认 true）、
  `rate_limit_per_key`、`verbose_access_log`；
  新增环境变量 `DOUBAO_TRUSTED_PROXIES`、`DOUBAO_ADMIN_LOOPBACK_ONLY`。
- Docker 模板加固：`read_only`、`tmpfs /tmp`、`cap_drop: ALL`、
  `no-new-privileges`、json-file 日志轮转；容器场景显式
  `DOUBAO_ADMIN_LOOPBACK_ONLY=false`。

### Added

- **上游错误分型**（`doubao.Kind`）：`ok / business / account / timeout / canceled / transport`。
  `/v1/*` 的状态码与 `error.type` 由分类决定（业务错误 422、超时 504、
  客户端断开 499 仅记日志）。`Stats` 新增 `canceled_requests` / `business_errors`，
  `LogEntry` 新增 `kind`——客户端主动断开不再抬高「上游失败率」。
- **流式预检**：`/v1/chat/completions` 与 `/v1/messages` 在产生第一帧后才提交
  SSE 头；上游在首帧前失败时返回普通 JSON 与真实状态码，不再出现
  「200 + 一个多数 SDK 不解析的 error 帧」。
- **总预算**：新增 `overall_timeout_sec`（默认 600s）。此前最坏耗时是
  `request_timeout × max_retries`（300×3=15 分钟），客户端早已放弃。
- **会话到期可见**：解析 `sid_guard` 推算 Cookie 到期时间，`/health` 输出
  `warnings[]`（`session_expiring` / `session_expired` / `checkin_pending_stale` /
  `no_api_keys` / `admin_disabled` / `admin_remote_access` / `cors_same_origin_only`）
  与 `degraded`；控制台账号页显示剩余天数。
- **`/readyz`**：没有可用账号时 503，供负载均衡使用；`/health` 保持永远 200。
- **访问日志**：每个请求一行 `request id=… path=… status=… kind=… ms=… acct=…`，
  错误文本过 `redact`（`sessionid` / `sid_guard` / `Authorization` / `Cookie` 等
  键的值整段替换为 `***`）。
- **状态备份**：每次保存保留 `<data>.json.bak`，按天生成 `backups/`（保留 7 份）；
  新增 `POST /admin/api/state/snapshot`、`GET /admin/api/state/export`、
  `POST /admin/api/state/import?confirm=1`（导入保留当前管理密码）。
- **合并落盘**：`Store.StartAutosave` 每 30 秒在有变更时落盘；账号失败计数、
  统计、密钥用量不再只在正常退出时保存。
- **账号池调度**：失败冷却（30s→30m 指数退避）、一次请求内不重复选同一账号
  （`NextAccountExcluding`）、重试退避抖动（200ms→2s）、单账号并发闸门
  （`max_concurrent_per_account`，默认 2）；选中时即更新 `LastUsed`，
  并发请求会分散到不同账号（此前 3 路并发会全部落在同一个号上）。
- `cmd/probe -models`：打印模型清单与上游 `model_item_key`，便于核对
  「名实相符」（issues.md #6）。

### Changed

- **模型清单与桌面端对齐**：`internal/registry` 按客户端缓存
  `_DesktopActionBarBriefListRequestCacheDataV1` 重写 —— 补齐 `自动`(9)、
  `豆包 2.1 Lite`(seed-lite-7b)、`Kimi K3.1 Pro 高速`、`GLM 5.3`、`GLM 5.3 Flash`，
  统一名称（`DeepSeek V4 Pro`）与上下文（256K/300K），并带上客户端推理档位。
  别名同步调整：`doubao-pro` → `doubao-2.1-pro`、`doubao-lite` → `doubao-2.1-lite`，
  新增 `glm` / `glm-flash` / `kimi-fast` / `seed-lite` 等。
  新增 `internal/doubao/client_models.go` 解析客户端 `Preferences` 缓存，
  `doubao2api-probe -client-models` 可打印各 profile 清单并与 registry 做差异比对
  （`TestRegistryCoversClientModels` 锁定清单，客户端升级后比对即可发现漂移）。
- `/v1/models` 的 `created` 改为固定的 `registry.CreatedAt`（此前每次请求取
  `time.Now()`，响应不可缓存）。
- `deepseek-v4-pro` 的展示名改为「DeepSeek V4 Pro（GA）」，与其上游键
  `person-deepseek-v4-pro-ga-260813` 对齐；新增 `registry` 审计测试，
  名称与键的版本号不一致会直接失败。
- 请求体上限统一为 `maxBodyBytes`（16MB），chat / anthropic 不再各用一套。
- HTTP 客户端补齐 `DialContext` / `TLSHandshakeTimeout` /
  `ResponseHeaderTimeout` / `ConnsPerHost`，避免建连黑洞占满总预算。
- `State` 新增 `meta`（`schema_version` / `created_at` / `booted_at`），
  `stats.started_at` 不再被误读成「本次部署时间」。
- 结构重构：`server` 拆为 `routes/auth/cors/errors/health/pool/trust/
  accesslog/admin_*`；`store` 拆为 `persist/accounts/keys/logs`；
  合并 `firstNonEmpty` / `firstNonEmptyStr` / `firstString` 三个重复实现。
- CI 新增覆盖率门槛 `scripts/coverage-gate.sh`（server ≥55%、doubao ≥32%、
  store ≥90%、account ≥90%）与 `FuzzReadSSE` 冒烟。

### Fixed

- 客户端断开（`context canceled`）被记成 502 失败并计入 `failed_requests`。
- 业务错误（`campaign not found`）以 502 返回，调用方无法判断「重试无意义」。
- 换号重试可能连续选中同一个账号（3 个账号实际只用 1 个）。
- 单账号一次限流后永久排到最后（现在按冷却时间自动恢复）。
- `tools` / `tool_choice` 被静默丢弃；现在默认返回 422
  `unsupported_parameter`（`tools_policy=ignore` 可恢复旧行为）。
- `stats.started_at` 与「本次部署」不符（来自导入的旧状态文件）。


### 实测（2026-11-14）

用 `npx lmfpd`（lm.ikale.io 官方 CLI，参考库 70 个模型）对网关做整数指纹检测，
每个模型 3 样本校准（单样本只给排名，噪声很大）：

| 模型 | 指纹最高匹配 | 置信度 | 结论 |
| --- | --- | ---: | --- |
| `deepseek-v4-flash` | deepseek-v4.1-flash-1017 | 99.3% | 名实相符 |
| `deepseek-v4-pro` | deepseek-v4-pro | 93.0% | 名实相符（此前单样本“结果分散”是噪声） |
| `auto` / `doubao-2.1-lite` / `doubao-2.1-pro` | seed-2.0-lite | 53%–90% | 符合预期（豆包 Seed） |
| `kimi-k3` / `kimi-k3.1-pro` | seed-2.0-lite | 50% / 98% | **未路由到 Kimi** |
| `kimi-k3.1-pro-fast` | mimo-v2.6-flash（Seed 并列） | 37% | **未路由到 Kimi** |
| `glm-5.3` / `glm-5.3-flash` | seed-2.0-lite | 单样本 | **未路由到 GLM** |

同时确认：`Default` / `Profile 2` 对工作模式模型稳定返回 `710022004 rate limited`，
实际只有 1 个账号在服务；`kimi-*` / `glm-*` 的 `model_item_key` 目前未被上游兑现
（需在桌面端确认账号权限或实验开关）。详见 `docs/recon.md`「模型指纹实测」。

## [0.1.3] - 2026-11-14

### Added

- **`/health` 暴露 `non_json_events`**：`internal/doubao` 新增 `ParseStats`，SSE 解析遇到
  无法解析的非 JSON `data:` 事件时计数（进程级累加）。此前这类事件被静默跳过，
  上游协议漂移时表现为「回答空白」而毫无告警；现在可通过对 `/health` 的观测
  发现计数异常增长。

### Changed

- **流式错误改为结构化帧**：`/v1/chat/completions` 在响应头提交后才遇到上游错误时，
  不再把 `[错误] …` 当作正文 delta 下发。现在输出一帧
  `{"error":{"type":"upstream_error","message":"…"},"choices":[{"delta":{},"finish_reason":"error"}]}`，
  再以 `data: [DONE]` 收尾。此前 OpenAI SDK 会把错误文案当正常回复，并在
  `finish_reason=stop` 时认为请求成功。
- **请求级 `agent_mode` 生效**：普通模型可以用 `agent_mode` / `doubao.agent_mode`
  显式开启工作模式；工作模式模型（registry 里带 `AgentMode` 的）自带值优先，
  不会被请求覆盖。此前该字段被合并进 `extras` 后从未使用，属于误导性死字段。

### Fixed

- **生成产物空 ID 被吞**：`newCreationURLs` 之前用 `seen[cr.ID]` 去重，creation 不带
  `id` 时首个产物就把 `seen[""]` 占位，后续不同 URL 的产物全被丢弃。现在 ID 为空时
  退化为按 URL 去重（同 URL 重复推送仍只下一次）。
- **未声明块类型无法兜底透传**：`handleUnknownBlock` 用 `blockKeyFor(bt)` 取
  `summary/title`，而真正未声明的类型该函数返回空串，导致兜底逻辑取不到值、
  注释承诺的「未知类型透传」实际不生效。现在键名为空时读取 content 顶层字段。
- **控制台图标尺寸过小**：`favicon.ico` 之前只有 16/32/48 三帧，高分屏标签页与
  任务栏会拿 48px 拉伸，显得模糊偏小。改为 16/24/32/48/64/128/256 **七档原生帧**
  并量化到 256 色调色板（133 KB → **32 KB**，与同类项目同一量级）；
  按 iOS 要求新增 180×180 `apple-touch-icon.png`（50 KB → 9 KB，iOS 不认 ICO），
  `assets/logo.png` 同步量化（86 KB → 14 KB）。
- **图标缓存语义**：从 `max-age=86400` 改为 **ETag + `Cache-Control: no-cache`**，
  命中 `If-None-Match` 回 304 —— 升级后不会继续显示旧图标，未变化时不重复下载
  （对齐同类项目的做法）；仍仅允许 GET/HEAD。
- 新增 `internal/server/console_favicon_test.go`：校验图标魔数 / Content-Type /
  nosniff / ETag / 304 / 405，并断言 ICO 含 256px 帧（防止回退成小尺寸）。
- **上游业务错误被当成账号故障**：`withAccountRetry` / `withAccount` 曾把任意上游错误
  都记到使用的账号上。线上实测后果：积分活动下线后 `/v1/tasks` 回
  `code=2 campaign not found`，三个账号全被标红、`/health` 报 `ready: 0`，
  且每个请求为同一错误换号重试 3 次白白多打上游。
  现在按错误归属分流（`internal/doubao/errors.go`）：
  - **账号级**（认证 / 会话 / 额度 / 限流 / HTTP 401/403）→ 记失败 + 换号重试
  - **业务/配置级**（`campaign not found`、`common invalid param` 等上游结构化业务码）
    → 立即返回，不记账号、不重试
  - 网络错误 / 超时 → 可重试，但不记账号
  同时把流式路径的上游错误改为 `*doubao.Error`，让分类不只依赖字符串匹配。
  验证：业务错误下 `ready` 保持 3、账号 `failures=0`、`tasks` 只产生 1 条日志；
  坏会话账号返回 `login invalid` 时该账号 `failures=1`、`ready` 降为 2。
- 新增测试：`internal/doubao/{sse,stream_blocks,blocks}_test.go` 与
  `internal/server/{chat,collector}_test.go`（54 个用例），覆盖 SSE 分帧边界、块分派/
  去重、思维链边界、非 JSON 计数，并用假上游（`rewriteTransport` 劫持到 `httptest`）
  跑通 `/v1/chat/*` 全链路：阻塞聚合、流式帧序、换号重试、附件失败、结构化错误帧。
  覆盖率：`doubao` 25.5% → 30.2%，`server` 34.5% → 47.3%，总计 36.5% → 42.8%。

## [0.1.2] - 2026-11-14

### Added

- **工作模式编排**：新增 `internal/workturn`（流 + 工具轮询 + 沙箱执行 + 结果回传），
  `cmd/probe -workturn <目录>` 可直接跑一次回合并 `-raw` 打印原始 agent 报文。
  实测：`agent_id` 获取、沙箱上报、轮询、正文汇总均正常；但第一次轮询即返回
  `{expired:true}`，上游未向本机投递工具调用。
  根因已定位到服务端 AB 开关：客户端仅当
  `enable_multi_env_client === "1"` 时才启用 dispatcher 投递循环，否则走 `sse`；
  该账号的 `/alice/user/config/pull` 对所有相关对象都返回 `configs: null`，
  即未命中实验。故**工具执行链路在本账号上不可完成**（非协议字段问题）。
- **工具执行端（沙箱运行时）**：新增 `internal/agent` —— 工作区受限的本地执行端，
  支持 `write_file` / `read_file` / `list_dir` / `run_command`；强制路径包含
  （拒绝绝对路径、盘符、`..` 与软链接逃逸）、读写与输出字节上限、命令默认关闭
  且仅白名单可执行文件、不经过 shell。含 12 个单测（越界/软链/超限/超时/白名单）。
- **工具通道客户端**：新增 `internal/doubao/tool_local.go` —— 封装
  `sandbox_info_report`、`msg/fetch_agent_miss`、`msg/ack`、`msg/complete`、
  `upload_tool_call_result(_v2)`。实测：沙箱上报 code 0；`agent_type` 必须传字符串；
  会话过期返回 `{expired:true}`（故编排必须与 SSE 流并发）。
- **工作模式（agent_mode=1）模型**：`/v1/models` 新增 `deepseek-v4-flash`
  （默认档）、`deepseek-v4-pro`、`kimi-k3`、`kimi-k3.1-pro`、`doubao-2.1-pro`，
  并带出上游 `model_item_key` 与 `agent_mode` 字段；别名 `deepseek` / `kimi` /
  `deepseek-flash` / `deepseek-pro` / `kimi-k3.1`。
  实测：经网关调用返回 200 + `conversation_id`，`kimi-k3` 流式 4 chunk 正常收尾。
  构造要点（三处 `model_item_key` 一致、`reasoning_effort` 只放 `conversation_init_ext`、
  不下发任何沙箱字段）见 `docs/recon.md` §6.12。
- `scripts/console-selftest.mjs`：用 CDP 驱动无头 Chrome 逐个点击侧栏并断言切页，
  作为控制台改动的回归手段（用法见 CONTRIBUTING）。
- `scripts/recon_pak_probe.py`（按关键字定位 pak 内 blob）与
  `scripts/recon_ctx.py`（打印 minified JS 的可读切片），供协议重新取证。
- 项目 Logo：从豆包客户端图标 `DoubaoWork/icon.ico` 取出原生 256×256 帧作为
  `assets/logo.png`；README 采用「居中 Logo + 标题 + 徽章」版式
  （CI / Release / Go / 零依赖 / 许可证 / API / 平台）。
- 控制台 favicon：同样取自 `DoubaoWork/icon.ico` 的 16/32/48 **原生帧**（不重新缩放，
  小尺寸更清晰），组装为 10 KB 的三帧 ICO 内嵌，由 `GET /favicon.ico` 提供
  （`public, max-age=86400`，仅 GET/HEAD），页面 head 加 `rel="icon"` 与
  `apple-touch-icon`。
- `.github/workflows/release.yml`：推送 `v*` tag 即自动发布 —— 校验 CHANGELOG 小节、
  跑 fmt / vet / `-race`、交叉构建三平台产物与 `SHA256SUMS`，然后建 Release
  （已存在则覆盖附件，兼容重跑）。
- README 新增「下载预编译产物」入口（含各平台文件名与校验方式）。
- `.markdownlint.yaml`：放行版式必需的内联 HTML（MD033 白名单），关闭
  MD041/MD013 并写明理由，其余规则保持默认。

### Changed

- `option.support_lazy_fetch_stream` 改为可选（`ChatRequest.SupportLazyFetchStream`
  与 `cmd/probe -lazy`）。A/B 实测同一长回复提示词：关闭 27.7s/160 块/1155 字，
  开启 25.2s/151 块/1084 字，两者都正常结束、无截断，差异属生成长度波动 ——
  该开关对本项目路径无明显收益，保持默认关闭。
- `cmd/probe` 新增 `-dump`：只打印将要发送的请求体，便于协议对照。
- 工作 / 智能体模式：按参考项目 README 的字段表实现后，上游仍返回
  `710020202 common invalid param`（6 轮组合均失败），已**回滚**实现。
  随后从 `entry-moa/main.js` 取证到真正阻塞点：`client_option` 里的
  `sandbox_id` / `sandbox_env_id` / `client_env_id` 必须来自**上游预创建的沙箱实例**
  （客户端先调沙箱创建接口拿 `instanceId`/`envId`），自造 UUID 必然被判非法参数；
  且 `agent_task_param` / `task_input_json` 等键在当前 147 bundle 里 0 命中，
  参考项目的写法不可照抄。结论：工作模式工具调用在 headless 网关不可复现。
  详见 `docs/recon.md` §6.12。
- 控制台：`:focus-visible` 焦点环（键盘可达）、切页回到顶部、启动时少发一次
  `/admin/api/state`（`init` 与 `showTab` 重复请求）。
- 协议取证更新（见 `docs/recon.md` §6.0）：147 版 `biz.pak` 只存编译产物，
  可读 JS 在 `gecko_cache` 与 `local_webcontents/apps`；公参新增可选的
  `update_version_code`、`tea_uuid` 实为独立 uuid；聊天走
  `/samantha/chat/completion`；客户端无静态模型清单（来自服务端 `moa_config`）；
  工具调用是本地沙箱 + `/alice/office/tool_local/upload_tool_call_result(_v2)`；
  `need_deep_think` / `action_bar_skill_id` 在当前客户端已不再出现。

### Fixed

- **控制台侧栏按钮"点了没反应"**：样式表里缺少 `.tab` / `.tab.on` 规则，导致 8 个
  `<section>` 页面全部堆叠显示（文档高 12614 px），点击导航只切换 class 而没有
  视觉变化。补上 `display:none/block` 后，用无头 Chrome + CDP 逐个点击 8 个按钮
  验证：面板正确切换、仅活跃面板可见、无控制台报错（页面高度 12614 → 1355）。
- `make dist` 的 Windows 产物此前未显式指定 `GOOS`：在 Linux runner 上会产出
  ELF 却命名为 `.exe`。现已固定 `GOOS=windows GOARCH=amd64`。
- CI 中的 `actions/checkout`、`actions/setup-go` 固定到 commit SHA；发布链路关闭
  checkout 的凭据持久化（避免 token 落进 `.git/config`）。
- CI 的 `setup-go` 缓存改用 `go.mod` 作为依赖键：本项目零第三方依赖、没有
  `go.sum`，原先每次都报「Dependencies file is not found」并放弃缓存。


## [0.1.0] - 2026-11-14

**首个正式版本。** 把本机豆包（DoubaoWork）桌面端的上游服务复刻成 OpenAI 兼容的
Go 网关：复用桌面端已登录会话，纯标准库实现，单文件约 10 MB，零第三方依赖。
本版本同时完成一轮架构收敛（运行时状态与持久化解耦、消除数据竞争）和
协议层回归测试建设。

### Added

#### 推理与能力端点

- OpenAI 兼容：`/v1/chat/completions`（SSE 流式 / 阻塞）、`/v1/models`、
  `/v1/images/generations`（文生图，OpenAI Images 形态）。
- Anthropic 兼容：`/v1/messages`（`message` 对象与 SSE 事件模型双向转换）。
- 豆包原生能力：`/v1/videos/generations`（创意画布文生视频）、
  `/v1/music/lyrics`、`/v1/music/audio`、`/v1/skills`、`/v1/capabilities`、
  `/v1/conversations*`（列表 / 重命名 / 归档 / 置顶 / 清空）。
- 积分与激励：`/v1/credits`、`/v1/credits/history`、`/v1/checkin`、`/v1/tasks`、
  `/v1/tasks/claim`、`/v1/tasks/claim-completed`、`/v1/tasks/run`。
- 结构化产物透传：`sources[]`（联网来源）、`artifacts[]`（文档 / 代码 / PPT）、
  `music[]`、`images[]`，阻塞式与流式响应都带。

#### 待确认签到的重试队列

- 可选开关 `retry_pending_checkin`（默认关闭）与后台队列：`/v1/checkin` 收到
  上游异步确认（`pending`）时把账号登记到状态文件，此后**每小时**复查一次，
  上游确认后自动补写签到日期与奖励额度。
- 队列策略：跨天记录作废（签到按天幂等）、当日最多重试 6 次、用尽后保留记录
  供控制台判断上游是否恢复（而不是悄悄丢弃）。
- `POST /admin/api/checkin/retry` 手动催办；`/admin/api/state` 与 `/v1/checkin`
  的 `account` 字段带出 `pending_checkin` 详情。
- 自动签到（`auto_checkin`）会跳过已登记进队列的账号，避免重复请求上游。
- 控制台：设置页开关、账号列表「待确认签到」列、积分页「催办签到」按钮、
  侧边栏「账号池 / 积分与任务」双入口角标（60s 轮询，仅更新角标不打断表单编辑）。

#### 控制台与部署

- 控制台单页（`internal/server/web/index.html`，单文件零外部资源，深浅色）：
  概览 / 账号池 / 密钥 / 模型与技能 / 积分与任务 / 会话 / 日志 / 设置。
- 扫码登录（passport SSO）、桌面端登录态导入（DPAPI + AES-GCM + 域名前缀剥离，
  支持多 profile）、手工 Cookie 录入与控制台连通性探活。
- 鉴权与安全默认值：Bearer / `x-api-key`、管理密码（`X-Admin-Password`）、
  无密钥模式仅放行本机、写操作同源校验、CORS 仅本机来源、恒定时间比较。
- Docker 一键部署（非 root、健康检查、默认只绑本机）与 `Makefile`
  （`build` / `check` / `race` / `cover` / `docker` / `probe`）。

#### 工程与文档

- `internal/ids`：随机标识叶子包（UUID / 十六进制 / 数字串）。
- 协议夹具回归：`internal/doubao/testdata/*.sse` + 表驱动测试，覆盖文本、
  思维链、搜索来源、代码块、生成产物、音乐与产物、业务错误、网关错误、
  内容补丁、工具提示。
- 测试：`registry` 覆盖率 0% → 96%，`store` ≈ 94%，`account` ≈ 95%，
  `server` ≈ 33%，`doubao` ≈ 22%；`store` 侧含 `-race` 并发回归。
- 文档：`docs/api.md`、`docs/recon.md`、`docs/security.md`、`docs/roadmap.md`、
  `docs/openapi.yaml`（30 路径 / 18 schema），以及 `SECURITY.md`、
  `CONTRIBUTING.md`、`CHANGELOG.md`。
- CI：gofmt / go vet / `go test -race` / 零依赖守卫 / Linux 构建与 Docker 构建。

### Changed

- **持久化层与协议层解耦**：账号模型移入 `internal/account`，
  `internal/store` 不再依赖 `internal/doubao`；状态文件格式不再随上游逆向结论漂移。
- **运行时状态不再落盘**：CSRF token 与 tab id 迁入进程内 `runtime`（副本间共享），
  状态文件不再写入 `csrf_token` / `csrf_token_at`。
- **账号快照语义**：`Accounts()` / `Account()` / `NextAccount()` 一律返回深拷贝，
  新增 `ToggleAccount` / `SetAccountEnabled` / `ClearAccountErrors` 作为写入口。
- `internal/server/extras.go`（845 行）按职责拆分为 `handlers_skills.go` /
  `handlers_conversations.go` / `handlers_credits.go` / `autorewards.go` /
  `upstream.go` / `collector.go`。
- 新增 `runUpstream`：选号、超时、日志、错误响应统一收口（`record` 调用点 36 → 8）。
- 新增 `responseCollector`：阻塞式与流式对话共用同一套产物累积 / 去重逻辑。
- 补丁流解析改为**块类型注册表**（`stream_blocks.go`），新增块类型只需注册一个函数。
- 领取结果分类按客户端语义修正：`biz_error.code` 的 `0/4/6` 为成功、
  `3` 为「确认中」（`pending` 并短重试）、其余为失败。

### Fixed

- **数据竞争**：store 曾把内部账号指针交给调用方，handler 在锁外改字段会与
  账号轮询、`Save()` 的 JSON 序列化并发读写同一块内存。现用「深拷贝 + 写方法」
  消除，并加入 `-race` 并发回归用例。
- **CSRF 缓存失效**：`Account.Clone()` 在原账号尚未初始化运行时状态时会给副本
  新建一份，导致每个请求都重新刷新 token（多一次上游往返）。
- **模型别名最长匹配**：`doubao-think-pro` 这类名字曾被短 ID `doubao` 抢先命中，
  解析为快速模式；现在按最长匹配 + 字典序取稳定结果。
- `/v1/models` 的自定义别名顺序不再随机（map 遍历顺序导致抖动）；自定义别名
  支持别名链（含环路保护与深度上限），并统一大小写归一化。
- **签到链路**：上游 `/alice/ug/campaign/landing/checkin` 已废弃（恒返回
  `710010702 system error`），改走活动任务路径（`daily_signin`：
  `event/report` 创建实例 + 奖励领取），旧接口保留为回退。
- `CampaignTask` 增加 `daily_checkin`（`checked_in_today` / `continuous_days` /
  `last_checkin_date` / `reward`），`/v1/tasks` 可直接判断今天是否已签到。
- `/v1/checkin` 只在 `checked_in && !pending` 时写入账号签到日期，
  避免「已受理待确认」被误记成今天已签到。
- 探测工具 `cmd/probe` 的响应打印不再按字节硬截断（会切断 `\uXXXX` 转义、
  输出非法 JSON），改为完整行边界截断并提示，新增 `-max` 控制上限。

### 发布前里程碑（未单独发版）

- 2025-10：桌面端登录态导入（DPAPI + AES-GCM + 域名前缀剥离）、上游协议复刻
  （公参 / 请求体 / SSE 补丁流）、OpenAI 兼容网关、控制台单页、Docker 部署、
  Apache-2.0 授权。
- 2025-11：扫码登录与控制台改版、积分 / 签到 / 任务奖励端点与后台自动签到、
  新块类型解析（搜索 / 代码 / 生图 / 产物 / 引用 / 音乐）、
  文生图 / 文生视频 / 音乐端点、Anthropic `/v1/messages` 兼容层。

### 已知限制

- 上传的协议结论基于 DoubaoWork `147.0.7727.149`；上游可能随时调整协议或启用风控。
- 签到奖励当前恒为上游「确认中」，`checked_in_today` 不会置真 —— 上游的签到动作
  已停止执行（详见 `docs/recon.md` §6.11）。网关如实返回 `pending`，
  并在开关打开时后台复查，上游恢复后无需改代码即可生效。
- 补丁流里非 JSON 的 `data:` 事件会被静默跳过（保持上游兼容），
  协议漂移时表现为回答为空而非报错。
