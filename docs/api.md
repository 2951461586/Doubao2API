# 接口契约

> OpenAI 兼容层 + 豆包原生扩展端点。机器可读版本见 [openapi.yaml](openapi.yaml)。
> 实现位置见 [recon.md](recon.md)，安全模型见 [security.md](security.md)。

## 支持的模型

豆包客户端**不按模型名路由**，而是用 `need_deep_think` 档位切换三种模式。
网关把档位映射为独立模型 ID，并对常见 OpenAI 模型名做别名兼容。

| 模型 ID | 档位 | 上下文 | 说明 |
| --- | --- | ---: | --- |
| `doubao` | 0（快速） | 128K | 默认，响应最快，无思维链 |
| `doubao-think` | 1（思考） | 128K | 返回思维链 → `reasoning_content` |
| `doubao-expert` | 3（专家） | 256K | 深度推理，适合复杂任务 |

内置别名：`doubao-pro` / `doubao-lite` / `doubao-fast` / `gpt-3.5-turbo` / `gpt-4o` / `gpt-4o-mini` / `gemini-pro` → `doubao`；
`doubao-reason` / `doubao-thinking` / `gpt-4` / `claude-3-5-sonnet` → `doubao-think`；
`doubao-max` / `doubao-deep` → `doubao-expert`。

还可在控制台「设置 → 模型别名」自定义，格式 `别名=目标`。

### 2.1 技能模型（选中即自动带 `action_bar_skill_id`）

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

### 2.2 推理强度

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
> 透传新版客户端的模型选择参数；`selected_skills` 对应 `ext.mention_skill_list`；
> `ext` 可整体透传上游 `ext`（高级用法）；也可写成 `{"doubao":{...}}` 命名空间形式。
> `content` 里可放 `input_file` / `image_url` 分片（见下文「多模态」）。
>
> 响应侧除 `content` / `reasoning_content` 外，还会在顶层（非流式）或 `delta`（流式）返回
> `images` / `sources` / `artifacts` / `music` 等结构化产物。

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
| `/health` `/healthz` | GET | 账号 / 模型就绪计数（免鉴权） |
| `/ping` | GET | 纯文本 `pong`（免鉴权，供容器探针） |

### 4.3 控制台接口

浏览器直接打开 <http://127.0.0.1:10086/> 即为内置控制台单页。控制台已改为侧边栏布局，
覆盖概览、账号池、密钥、模型与技能、积分与任务、会话、日志、设置；支持深浅色切换、
管理密码记忆、扫码登录、签到 / 积分 / 任务操作。单文件内嵌、无外部依赖，调用的就是下面这些 JSON 接口：

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/admin/api/state` | GET | 全量状态（账号 / 密钥 / 模型 / 统计，**凭据已脱敏**） |
| `/admin/api/accounts` | POST | `import` / `manual` / `remove` / `toggle` / `clear-errors` |
| `/admin/api/keys` | GET / POST / DELETE | 密钥增删与启停 |
| `/admin/api/logs` | GET / DELETE | 请求日志 |
| `/admin/api/stats` | GET / DELETE | 统计 |
| `/admin/api/settings` | GET / POST | 设置 |
| `/admin/api/checkin` | POST | 对账号做连通性探测 |
| `/admin/api/login/qrcode` | POST | 发起扫码登录，返回 `qrcode`（data URL）与 `session_id` |
| `/admin/api/login/qrcode/poll` | POST | 轮询扫码状态：`waiting` / `scanned` / `confirmed` / `expired` |

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

#### 管理接口鉴权

`/admin/api/*` 受两层保护：

1. **身份**：未设置管理密码时**仅允许本机环回**访问（`127.0.0.1` / `::1`）；
   设置了管理密码后，必须携带 `X-Admin-Password: <密码>`
   或 `Authorization: Bearer <密码>`。密码以恒定时间比较。
2. **CSRF**：写操作（非 GET/HEAD/OPTIONS）额外做同源校验——浏览器带
   `Origin` 时，必须与请求 Host 同源或命中 CORS 允许列表；非浏览器
   客户端（curl / SDK）通常不带 `Origin`，不受影响。

密钥始终以脱敏形式返回（`sk-abc...wxyz`），**完整密钥仅在创建时返回一次**；
`/admin/api/settings` 也只回传 `admin_password_set` 布尔值，不回传密码明文。

```bash
# 设置管理密码（未设置密码时仅本机可操作）
curl -X POST http://127.0.0.1:10086/admin/api/settings \
  -H "Content-Type: application/json" -d '{"admin_password":"换成你的密码"}'

# 之后所有管理调用都要带密码
curl http://127.0.0.1:10086/admin/api/state \
  -H "X-Admin-Password: 换成你的密码"
```

---
