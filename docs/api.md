# 接口契约

> OpenAI 兼容层 + 豆包原生扩展端点。机器可读版本见 [openapi.yaml](openapi.yaml)。
> 实现位置见 [recon.md](recon.md)，安全模型见 [security.md](security.md)。

## 支持的模型

模型清单与桌面端「工作任务」列表**逐项对齐**（名称、`model_item_key`、上下文），
数据来源是客户端缓存 `_DesktopActionBarBriefListRequestCacheDataV1`，
可用 `doubao2api-probe -client-models` 复核差异。

### 2.0 工作模式模型（`agent_mode=1`）

| 模型 ID | 客户端名称 | model_item_key | 上下文 |
| --- | --- | --- | ---: |
| `auto` | 自动 | `9` | 256K |
| `doubao-2.1-lite` | 豆包 2.1 Lite | `seed-lite-7b` | 256K |
| `doubao-2.1-pro` | 豆包 2.1 Pro | `5` | 256K |
| `deepseek-v4-flash` | DeepSeek V4.1 Flash | `person-deepseek-v4-1-flash-261017` | 300K |
| `kimi-k3.1-pro` | Kimi K3.1 Pro | `person-kimi-k3-1-pro-261111` | 300K |
| `kimi-k3.1-pro-fast` | Kimi K3.1 Pro 高速 | `person-kimi-k3-1-pro-highspeed-261111` | 300K |
| `kimi-k3` | Kimi K3 | `person-kimi-k3-261111` | 300K |
| `glm-5.3` | GLM 5.3 | `person-glm-5-3-261111` | 300K |
| `glm-5.3-flash` | GLM 5.3 Flash | `person-glm-5-3-flash-261111` | 300K |
| `deepseek-v4-pro` | DeepSeek V4 Pro | `person-deepseek-v4-pro-ga-260813` | 300K |

`/v1/models` 的 `doubao.reasoning_levels` 给出客户端为该模型提供的推理档位
（如 `["低","中","高"]`）。档位名只是展示；实际强度仍由请求的
`reasoning_effort` 决定（`low` → 快速、`medium` → 思考、`high` → 专家）。

> `自动` 的上下文在不同 profile 的缓存里可能是 256K 或 300K（服务端按实验下发），
> 网关取保守值 256K。
>
> 清单只代表「客户端允许选择这些模型」。上游是否真的路由到它们，需以指纹实测为准：
> 2026-11-14 实测 `deepseek-*` 与豆包系列命中，Kimi / GLM 未兑现（详见
> [recon.md](recon.md#模型指纹实测2026-11-14)）。

### 2.1 普通对话三档（`need_deep_think`）

旧客户端不按模型名路由，而是用 `need_deep_think` 档位切换三种模式；
网关把这些档位也保留为独立模型 ID。

| 模型 ID | 档位 | 上下文 | 说明 |
| --- | --- | ---: | --- |
| `doubao` | 0（快速） | 128K | 默认，响应最快，无思维链 |
| `doubao-think` | 1（思考） | 128K | 返回思维链 → `reasoning_content` |
| `doubao-expert` | 3（专家） | 256K | 深度推理，适合复杂任务 |

内置别名：`doubao-pro` → `doubao-2.1-pro`、`doubao-lite` → `doubao-2.1-lite`、
`doubao-auto` → `auto`、`doubao-fast` / `gpt-3.5-turbo` / `gpt-4o` / `gpt-4o-mini` /
`gemini-pro` → `doubao`；`doubao-reason` / `doubao-thinking` / `gpt-4` /
`claude-3-5-sonnet` → `doubao-think`；`doubao-max` / `doubao-deep` → `doubao-expert`；
`deepseek` / `deepseek-flash` → `deepseek-v4-flash`，`deepseek-pro` → `deepseek-v4-pro`；
`kimi` → `kimi-k3`，`kimi-k3.1` → `kimi-k3.1-pro`，`kimi-fast` →
`kimi-k3.1-pro-fast`；`glm` → `glm-5.3`，`glm-flash` → `glm-5.3-flash`。

还可在控制台「设置 → 模型别名」自定义，格式 `别名=目标`。

### 2.2 技能模型（选中即自动带 `action_bar_skill_id`）

| 模型 ID | 技能 | 说明 |
| --- | ---: | --- |
| `doubao-draw` | 3 | 图像生成（Seedream） |
| `doubao-search` | 4 | AI 搜索（联网） |
| `doubao-write` | 2 | 创意写作 |
| `doubao-translate` | 5 | 翻译 |
| `doubao-read` | 6 | PDF / 文档阅读 |
| `doubao-code` | 0 | Coding |
| `doubao-data` | 13 | 代码解释器 / 数据分析 |
| `doubao-deep-search` | 8 | 多轮深度检索 |
| `doubao-research` | 15 | 深度研究报告 |
| `doubao-ppt` | 18 | AI PPT |
| `doubao-music` | 9 | 音乐生成（需配额） |
| `doubao-video` | 17 | 视频生成（需配额） |

技能模型会自动套用上游 `default_prompt`（如 `生成一张图片:${style} ${content}`），
提示词里已带模板前缀时不会重复包裹。完整技能清单见 `GET /v1/skills`。

### 2.3 推理强度

聊天接口接受 OpenAI 的 `reasoning_effort`，映射到上游 `need_deep_think`：
`none/minimal/low` → 0（快速），`medium/auto/default` → 1（思考），
`high/xhigh/max` → 3（专家）。显式传 `skill` / 技能模型时，技能优先级更高。

> 无法识别的模型名会**回落到 `doubao`**，而不是报错——这样任何客户端都能开箱即用。

---

## 端点

### 4.1 推理接口

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/v1/chat/completions` | POST | OpenAI Chat Completions，流式 / 非流式 |
| `/v1/messages` | POST | Anthropic Messages，流式 / 非流式 |
| `/v1/images/generations` | POST | OpenAI Images，文生图（返回 `url` / `b64_json`） |
| `/v1/videos/generations` | POST | 文生视频（返回 `url` / `vid` / 时长 / 分辨率） |
| `/v1/music/lyrics` | POST | 生成歌词（`prompt` / `genre` / `mood`） |
| `/v1/music/audio` | POST | 用音乐消息的 `video_id` 换取音频地址 |
| `/v1/skills` | GET | 上游技能清单（含默认提示词 / 别名 / 对应模型） |
| `/v1/conversations` | GET | 会话列表（`limit` / `offset` / `types`） |
| `/v1/conversations/{id}` | PATCH / DELETE | 重命名 / 归档；`POST .../pin`、`/unpin`、`/archive`、`/clear` |
| `/v1/credits` | GET | 积分余额 + 每日登录任务（`?tasks=false` 可省略任务） |
| `/v1/credits/history` | GET | 积分流水（`size` / `cursor` / `type`） |
| `/v1/checkin` | POST | 每日签到（返回 `checked_in` / `pending` / 奖励额度） |
| `/admin/api/checkin/retry` | POST | 立即复查「确认中」的签到（仍受 `retry_pending_checkin` 约束） |
| `/v1/tasks` | GET | 积分任务 / 里程碑（默认 `pc_incentive_task_mvp_v3`） |
| `/v1/tasks/claim` | POST | 领取单个任务奖励 |
| `/v1/tasks/claim-completed` | POST | 自动领取「已完成 / 直接领取」的任务奖励 |
| `/v1/tasks/run` | POST | 自动尝试执行任务（推荐提示词 + WorkTask 模式）并清理会话 |
| `/v1/capabilities` | GET | 模型 / 技能 / 端点自发现 |
| `/v1/models` | GET | 模型清单（含 `doubao.think_level` / `doubao.skill` 扩展字段） |

鉴权：`Authorization: Bearer <sk-...>` 或 `x-api-key`。
**未签发任何密钥时只允许本机无密钥调用**（首次使用的便利）：非环回来源会被拒绝，
除非在设置里显式开启 `allow_anonymous`；一旦在控制台创建密钥即强制校验。

> **扩展字段**：聊天接口支持 `skill`（技能透传），可取名称 / 别名（`image` / `search` / `write` / `music` / `video` / `ppt` …）
> 或直接传数值（0 / 2 / 3 / 4 / 5 / 6 / 8 / 9 / 13 / 15 / 17 / 18 …），对应上游 `option.action_bar_skill_id`；
> `input_skill` 对应上游 `ext.input_skill`（音乐技能入参）；
> `reasoning_effort` 映射思考档位；`agent_mode` / `model_item_key` / `mode_id` / `selection_mode`
> 透传新版客户端的模型选择参数（`agent_mode` 对普通模型生效；工作模式模型自带的固定值优先）；
> `selected_skills` 对应 `ext.mention_skill_list`；
> `ext` 可整体透传上游 `ext`（高级用法）；也可写成 `{"doubao":{...}}` 命名空间形式。
> `content` 里可放 `input_file` / `image_url` 分片（见下文「多模态」）。
>
> 响应侧除 `content` / `reasoning_content` 外，还会在顶层（非流式）或 `delta`（流式）返回
> `images` / `sources` / `artifacts` / `music` 等结构化产物。
>
> 流式请求在响应头已提交后才遇到上游错误时，会下发一帧结构化错误：
> `{"error":{"type":"upstream_error","message":"…"},"choices":[{"delta":{},"finish_reason":"error"}]}`，
> 随后以 `data: [DONE]` 收尾；不会再以 `finish_reason=stop` 伪装成功。

#### 多轮会话

两种方式都支持：

1. **客户端带全量历史**（OpenAI 标准做法）——网关把历史压平进单轮提示，语义等价。
2. **`conversation_id` 原生续接**（豆包服务端维护上下文）：

```bash
# 第一轮：不传 conversation_id，响应中返回新 ID
curl ... -d '{"model":"doubao","messages":[{"role":"user","content":"我叫小明"}]}'
# → {"conversation_id":"38445411928226562", ...}

# 第二轮：带上它
curl ... -d '{"model":"doubao","conversation_id":"38445411928226562",
             "messages":[{"role":"user","content":"我叫什么名字？"}]}'
# → "你叫小明。"
```

流式响应中，`conversation_id` 通过 SSE 注释块回传：`: conversation_id=3844...`。

#### 多模态（图片 / 文件）

图片：`content` 分片里放 `image_url`，两种写法都支持：

```jsonc
{"role":"user","content":[
  {"type":"text","text":"图里有什么？"},
  // 1) 内联 data URL
  {"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR..."}},
  // 2) 公网外链（网关代下载后转存）
  {"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
]}
```

文件（PDF / Office / 文本）：用 `input_file`（或 `file`）分片，同样支持 data URL 与裸 base64：

```jsonc
{"role":"user","content":[
  {"type":"text","text":"这个文件里的验证码是什么？"},
  {"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBER..."}
]}
```

**上游不会自行抓取外链**（实测会回「请上传图片」），因此网关先把附件转存到豆包资源中心，
再把得到的 `uri` 填进 `block_type=10052` 附件块（图片 `type=1`、文件 `type=3`）。
外链大小上限 10 MB。Anthropic 侧对应的 `image` / `document` 块走同一条链路。

#### Anthropic `/v1/messages`

同一套上游能力也以 Anthropic 协议暴露，便于 Anthropic SDK / Claude Code 直连：

```bash
curl http://127.0.0.1:10086/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model":"doubao-think","max_tokens":512,"stream":true,
       "system":"你是简洁助手。",
       "messages":[{"role":"user","content":"用一句话解释 SSE。"}]}'
```

| Anthropic 概念 | 映射 |
| --- | --- |
| `system`（字符串或文本块数组） | 压平为提示前缀 |
| `messages[].content` 文本块 | 正文 |
| `content[].type=image`（`base64` / `url`） | 转存上游 → `block_type=10052` |
| `tool_result` | 按文本回流；`tool_use` / `thinking` 不回灌 |
| 思维链 | 流式为 `thinking` 块 + `thinking_delta` |
| SSE 事件 | `message_start` → `content_block_start/delta/stop` → `message_delta` → `message_stop` |
| 错误 | `{"type":"error","error":{"type":"api_error"\|"invalid_request_error",…}}` |

`max_tokens` / `temperature` / `top_p` / `stop_sequences` 目前不透传（豆包不按这些路由）。

### 4.2 探活接口

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/health` `/healthz` | GET | 永远 200；附 `degraded` / `warnings[]` / `non_json_events`（免鉴权） |
| `/readyz` | GET | 就绪检查：没有可用账号时返回 503（供负载均衡使用） |
| `/ping` | GET | 纯文本 `pong`（免鉴权，供容器探针） |

`/health` 响应示例：

```json
{
  "status": "ok",
  "degraded": true,
  "accounts": 3,
  "ready": 3,
  "models": 20,
  "non_json_events": 0,
  "started_at": 1763000000,
  "booted_at": 1763100000,
  "session_warn_days": 14,
  "warnings": [
    {"code": "session_expiring", "account_id": "…", "account": "DoubaoWork/Default",
     "expires_at": 1764666933, "days_left": 17, "message": "会话 Cookie 即将过期，请提前重新登录并导入"},
    {"code": "cors_same_origin_only", "message": "未配置 CORS 来源：浏览器跨域直连会被拦截（curl / SDK 不受影响）"}
  ]
}
```

告警 `code` 一览：

| code | 含义 | 处理 |
| --- | --- | --- |
| `session_expiring` | Cookie 在 `session_warn_days` 内到期 | 重新登录并导入 |
| `session_expired` | Cookie 已过期 | 同上 |
| `checkin_pending_stale` | 有跨天未确认的签到 | 开启 `retry_pending_checkin` |
| `no_api_keys` | 未签发密钥 | 控制台创建，或显式 `allow_anonymous` |
| `admin_disabled` | 未设置管理密码 | 设置 `DOUBAO_ADMIN_PASSWORD` |
| `admin_remote_access` | 管理面允许非本机访问 | 确认前面是可信代理并配 `trusted_proxies` |
| `cors_same_origin_only` | 未配置 CORS | 需要浏览器直连时配置来源 |

> `/health` 永远 200：实例活着就不该被摘掉。要判断「能否服务」用 `/readyz`。

### 4.3 控制台接口

浏览器直接打开 <http://127.0.0.1:10086/> 即为内置控制台单页。控制台已改为侧边栏布局，
覆盖概览、账号池、密钥、模型与技能、积分与任务、会话、日志、设置；支持深浅色切换、
管理密码记忆、扫码登录、签到 / 积分 / 任务操作。单文件内嵌、无外部依赖，调用的就是下面这些 JSON 接口：

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/api/state` | GET | 全量状态（账号 / 密钥 / 模型 / 统计，**凭据已脱敏**） |
| `/admin/api/accounts` | POST | `import` / `manual` / `remove` / `toggle` / `clear-errors` |
| `/admin/api/keys` | GET / POST / DELETE | 密钥增删与启停（按 `id` 操作；列表只回展示片段） |
| `/admin/api/logs` | GET / DELETE | 请求日志 |
| `/admin/api/stats` | GET / DELETE | 统计 |
| `/admin/api/settings` | GET / POST | 设置 |
| `/admin/api/checkin` | POST | 对账号做连通性探测 |
| `/admin/api/login/qrcode` | POST | 发起扫码登录，返回 `qrcode`（data URL）与 `session_id` |
| `/admin/api/login/qrcode/poll` | POST | 轮询扫码状态：`waiting` / `scanned` / `confirmed` / `expired` |
| `/admin/api/state/snapshot` | POST | 立即生成带时间戳的状态备份 |
| `/admin/api/state/export` | GET | 下载可迁移状态（不含请求日志） |
| `/admin/api/state/import` | POST | 导入状态，必须 `?confirm=1`；`?merge=1` 只合并账号 |

#### 管理接口鉴权

`/admin/api/*` 受三层保护：

1. **必须先设置管理密码**：未设置时管理接口一律返回 `503`
   （此前「未设密码则只允许本机」的退路在反代场景会被绕过，已移除）。
   密码以 **PBKDF2-HMAC-SHA256** 哈希落盘，校验用恒定时间比较。
2. **来源**：`admin_loopback_only=true`（裸机默认）时只接受本机直连；
   容器 / 反向代理部署需显式关闭，并配置 `trusted_proxies`。
   带 `X-Forwarded-For` 但来源不在可信网段时，一律按远程处理。
3. **爆破防护**：同一来源 5 分钟内失败 5 次返回 `429` + `Retry-After`。
   写操作（非 GET/HEAD/OPTIONS）另做同源校验以阻断 CSRF。

密钥只保存 **SHA-256 指纹**与展示片段，**完整密钥仅在创建时返回一次**；
`/admin/api/settings` 只回传 `admin_password_set` 布尔值。

扫码登录（摆脱对本机桌面端的依赖）：

```bash
SID=$(curl -s -X POST http://127.0.0.1:10086/admin/api/login/qrcode | jq -r .session_id)
# 用「抖音 APP」扫描返回的 qrcode 图片并在手机上确认，然后轮询：
curl -s -X POST http://127.0.0.1:10086/admin/api/login/qrcode/poll \
  -H 'Content-Type: application/json' -d "{\"session_id\":\"$SID\"}"
# → {"status":"confirmed","id":"...","name":"扫码登录 01-02 15:04","new":true}
```

> 豆包账号体系走**抖音 SSO**，扫码需在抖音 APP 内完成；确认后网关自动换取 Cookie 并入库。
> 控制台「账号」页也内置了扫码按钮。

```bash
# 设置管理密码（必须先设置，否则管理接口不可用）
curl -X POST http://127.0.0.1:10086/admin/api/settings \
  -H "Content-Type: application/json" -d '{"admin_password":"换成你的密码"}'

# 之后所有管理调用都要带密码
curl http://127.0.0.1:10086/admin/api/state \
  -H "X-Admin-Password: 换成你的密码"
```

---

## 错误与状态码

上游失败按**可操作性**分五类，`/v1/*` 的状态码与 `error.type` 都由分类决定：

| 分类 | `error.type` | HTTP | 含义 | 客户端应做什么 |
| --- | --- | --- | --- | --- |
| ok | — | 2xx | 成功 | — |
| business | `business_error` | 422 | 上游业务 / 配置错误（如 `campaign not found`） | 不要重试，检查参数或活动是否下线 |
| account | `account_error` | 502 | 会话 / 额度 / 风控；已自动换号重试 | 检查账号状态与 `/health` |
| timeout | `timeout_error` | 504 | 单次尝试或总预算超时 | 可重试；确认 `overall_timeout_sec` |
| canceled | `client_closed_request` | 499（仅日志） | 客户端主动断开 | 无需处理 |
| transport | `upstream_error` | 502 | 网络 / 解析 / 非预期响应 | 可重试 |

要点：

- **客户端断开不计入失败率**：日志 `kind=canceled`，统计里单独记
  `canceled_requests`，不会污染 `failed_requests`。
- **业务错误不标红账号**：`campaign not found` 换任何账号都一样，
  因此只记 `business_errors`，账号保持 `ok`。
- **流式请求先探测再提交**：上游在首帧之前就失败时返回普通 JSON 错误
  （真实状态码）；已提交响应头后才失败则下发结构化 error 帧：

  ```json
  {"error":{"type":"business_error","message":"…"},
   "choices":[{"index":0,"delta":{},"finish_reason":"error"}]}
  ```

  随后仍以 `data: [DONE]` 收尾。
- **工具字段默认拒绝**：请求里带 `tools` / `tool_choice` 时返回 422
  `unsupported_parameter`（把 `tools_policy` 设为 `ignore` 可恢复旧行为）。

### 超时与重试

| 设置 | 默认 | 作用 |
| --- | --- | --- |
| `request_timeout_sec` | 300 | 单次上游尝试 |
| `overall_timeout_sec` | 600 | 一次客户端请求的总预算（含换号、退避） |
| `max_retries` | 3 | 最多尝试几个**不同**账号 |
| `max_concurrent_per_account` | 2 | 单账号并发闸门 |

失败换号之间做 200ms→2s 的指数退避（含抖动），并给失败账号设置
30s→30m 的冷却；冷却中的账号不会被选中（全部冷却时回退到最少失败者）。
