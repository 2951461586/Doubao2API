# Doubao2API

把本机 **豆包工作（DoubaoWork）** 桌面端的上游模型服务，逆向复刻成一个
**OpenAI 兼容的 Go 网关**。

复用桌面端已登录的会话凭据，无需另申请密钥；纯 Go 标准库实现，单文件约 10 MB，
零外部依赖。

```text
┌──────────────┐   /v1/chat/completions   ┌─────────────┐   Cookie + CSRF 头   ┌────────────────────────┐
│ 客户端        │   /v1/models             │ Doubao2API  │  ──────────────────▶ │ www.doubao.com         │
│ OpenAI SDK   │ ───────────────────────▶ │  (本网关)    │  ◀────────────────── │ /chat/completion       │
│ NewAPI / pi  │   SSE 流式 / 非流式       │             │      SSE 流式        │                        │
└──────────────┘                          └──────┬──────┘                      └────────────────────────┘
                                                 │ 自动读取并解密
                                                 ▼
                          %LOCALAPPDATA%\DoubaoWork\User Data\<profile>\Network\Cookies
                          %LOCALAPPDATA%\DoubaoWork\User Data\Local State
```

---

## 一、逆向结论：链路很短，但凭据加密有两层

DoubaoWork 是**基于 Lark/飞书内核的 Chromium 原生应用**（不是 Electron）：
主逻辑打包在 `local_webcontents/biz/biz.pak`（224 MB 自定义包），
AI 能力由内部代号 **`samantha`** 的服务提供，账号资料走 **`alice`**。

推理入口本身是一个标准的 SSE 接口，**不需要伪造签名、不需要设备指纹算法、
不需要驱动桌面客户端**——只要能拿到那份 Cookie。

| 环节 | 逆向结果 |
| --- | --- |
| 应用形态 | Chromium 原生应用（Lark 内核），`biz.pak` = 12 字节头 + 6 字节索引 + N 个 blob |
| 业务代号 | `samantha`（AI 会话）、`alice`（账号 / 资料） |
| **推理入口** | `POST https://www.doubao.com/chat/completion` |
| **鉴权** | Cookie（`sessionid` / `sid_guard` / `ttwid` / `odin_tt`）+ 头 `x-tt-passport-csrf-token` |
| 公参 | `aid=582478`、`device_platform=web`、`version_code=20800`、`fp`、`web_id`、`web_tab_id` |
| 请求体 | `client_meta` + `messages[].content_block` + `option` + `ext`（JSON 明文） |
| 响应 | SSE：`SSE_ACK` → `STREAM_MSG_NOTIFY` → `STREAM_CHUNK` / `CHUNK_DELTA` → `SSE_REPLY_END` |
| 凭据落盘 | `<User Data>\<profile>\Network\Cookies`（**SQLite**） |
| 凭据加密 | **两层**：DPAPI 保护的主密钥（`Local State`）+ AES-256-GCM 逐条加密 |
| 域名绑定 | Chrome 130+ 起，Cookie 明文前置 32 字节 `sha256(host_key)`，解密后须剥离 |

### 1.1 `biz.pak` 包格式与还原流程

```text
header : version(u32 LE) | reserved(u32) | count(u32)
index  : count × { id(u16 LE) | offset(u32 LE) }   // 按 offset 升序
data   : 第 i 个 blob = [offset_i, offset_{i+1})，多为 gzip
```

本机实测 `count = 10368`。还原分两步（均可随时重跑）：

```bash
# 1) 拆包：把 pak 拆成一堆 blob（约 580 MB，产物勿入库）
python tools/extract_pak.py "F:\IDE\DoubaoWork\app\local_webcontents\biz\biz.pak" .recon/extract

# 2) 还原源码：解出 35 份 source map 的 sourcesContent，按原目录结构落盘
python tools/extract_sources.py .recon/extract .recon/src --stats
```

路径映射规则：`webpack://<bundle>/../../X` → `_/_/X`，
`../../../node_modules/x` → `_/_/_/nm/x`（即 `..` → `_`、`node_modules` → `nm`）。

> `extract_sources.py` 在 Windows 下用 `\\?\` 前缀绕开 MAX_PATH，否则 `.pnpm`
> 里那些超长路径会被静默跳过（实测能多还原 46 个文件）。

### 1.2 关键代码位置

`biz.pak` 内含 35 份 **source map**，其中 `sourcesContent` 保存了原始 TypeScript，
实测可还原 **3010** 个源文件。几个决定性的位置：

| 事实 | 位置 |
| --- | --- |
| 上游入口与请求头 | `static/js/*.js`：`/samantha/chat/completion`、`Agw-Js-Conv: str` |
| 请求体构造 | `L1-Arch/business/api/src/flow-api.ts` 及各 `api/*/index.ts` |
| 公参定义 | `src/init/params/index.ts`：`aid` / `device_id` / `web_id` / `version_code` |
| 应用常量 | `Infra/constants/src/index.ts`：`VERSION_CODE=20800`、`DESKTOP_DOUBAO_WORK_APP_ID=1044603` |
| 内容块枚举 | `block_type`：`10000` 文本、`10040` 思考、`10024` 工具、`10025` 联网搜索、`10052` 附件、`2074` 生成结果 |
| 技能枚举 | `SkillType`：`3` 图像、`9` 音乐、`17` 视频（即 `option.action_bar_skill_id`） |
| 画布操作枚举 | `canvas_type`：`GenImage=10`、`GenVideo=50`（创意画布执行接口） |
| 消息内容类型 | `content_type`：`70` 音乐成品、`71` 歌词、`2021` 视频生成、`2074` 生成结果 |

### 1.3 与参考项目 AStudio2API 的对照

| 维度 | AStudio2API | 本项目 |
| --- | --- | --- |
| 应用形态 | Electron（`app.asar`） | Chromium 原生（Lark 内核，`biz.pak`） |
| 凭据落盘 | 明文 JSON | **SQLite + DPAPI + AES-GCM + 域名绑定前缀** |
| 凭据解析 | `encoding/json` | 手写极简 SQLite b-tree 解析器（保持零依赖） |
| 上游协议 | OpenAI 原生透传 | 私有 SSE（`content_block` 补丁流） |
| 协议转换 | 基本透传 | 需把补丁流还原为文本 / 思维链 |
| 上游鉴权 | `Bearer` | Cookie + CSRF 头 |

---

## 二、支持的模型

豆包客户端**不按模型名路由**，而是用 `need_deep_think` 档位切换三种模式。
网关把档位映射为独立模型 ID，并对常见 OpenAI 模型名做别名兼容。

| 模型 ID | 档位 | 上下文 | 说明 |
| --- | --- | ---: | --- |
| `doubao` | 0（快速） | 128K | 默认，响应最快，无思维链 |
| `doubao-think` | 1（思考） | 128K | 返回思维链 → `reasoning_content` |
| `doubao-expert` | 3（专家） | 256K | 深度推理，适合复杂任务 |

内置别名：`doubao-pro` / `doubao-lite` / `doubao-fast` / `gpt-3.5-turbo` / `gpt-4o` → `doubao`；
`doubao-reason` / `doubao-thinking` / `gpt-4` → `doubao-think`；
`doubao-max` / `doubao-deep` → `doubao-expert`。

还可在控制台「设置 → 模型别名」自定义，格式 `别名=目标`。

> 无法识别的模型名会**回落到 `doubao`**，而不是报错——这样任何客户端都能开箱即用。

---

## 三、快速开始

### 3.1 编译与运行

```bash
go build -ldflags="-s -w" -o doubao2api .
./doubao2api
```

默认监听 `127.0.0.1:10086`。首次启动会自动从本机 DoubaoWork 导入登录态：

```text
2026/01/01 09:00:00 状态文件: ...\doubao2api-data.json
2026/01/01 09:00:00 已导入账号 DoubaoWork/Default（profile=Default，Cookie 37 项）
2026/01/01 09:00:00 Doubao2API 已启动: http://127.0.0.1:10086/
```

### 3.2 只导入一次

```bash
./doubao2api -import          # 自动探测并导入后退出
./doubao2api -import -doubao-dir "D:\path\to\User Data"
```

### 3.3 调用

```bash
curl http://127.0.0.1:10086/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"doubao-think","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

### 3.4 Docker 一键部署

镜像只打包一个静态 Go 二进制（≈ 6 MB），构建阶段不需要联网拉依赖
（纯标准库，`go.mod` 无 require）。

```bash
# 1) 构建并启动
docker compose up -d --build

# 2) 看日志
docker compose logs -f

# 3) 打开服务
#    http://127.0.0.1:10086/health
```

也可以直接 `docker run`：

```bash
docker build -t doubao2api:latest .

docker run -d --name doubao2api --restart unless-stopped \
  -p 127.0.0.1:10086:10086 \
  -v "$PWD/data:/data" \
  -e TZ=Asia/Shanghai \
  doubao2api:latest -no-import
```

要点：

| 项 | 说明 |
| --- | --- |
| 状态卷 | `./data:/data`，`DOUBAO_DATA_PATH=/data/doubao2api-data.json` |
| **卷里含凭据** | `doubao2api-data.json` 等同账号密码，**不要提交或分享** |
| 端口 | compose 默认绑 `127.0.0.1`；要局域网访问改成 `"10086:10086"` |
| 时区 | `TZ=Asia/Shanghai`，否则日志与统计按 UTC 算 |
| 出网 | 容器需能访问 `www.doubao.com` |
| 健康检查 | `GET /ping`（免鉴权） |
| 自动导入 | 容器内**不可用**（DPAPI 是 Windows 专有），故用 `-no-import` 启动 |
| 管理密码 | 通过 `DOUBAO_ADMIN_PASSWORD` 设置；不设置则管理接口仅回环可达 |

#### 容器内如何添加账号

容器里没有桌面端，因此**不能自动导入**，改用「手工录入 Cookie」：

1. 在**有桌面端的机器**上登录豆包，用浏览器 DevTools 或导出工具
   复制 `.doubao.com` 域的 Cookie 串，至少包含
   `sessionid`（或 `sid_guard`）、`ttwid`、`passport_csrf_token`。
2. POST 到控制台接口：

    ```bash
    curl -X POST http://127.0.0.1:10086/admin/api/accounts \
      -H "Content-Type: application/json" \
      -H "X-Admin-Password: $DOUBAO_ADMIN_PASSWORD" \
      -d '{
            "action": "manual",
            "name": "我的账号",
            "cookies": "sessionid=xxx; sid_guard=xxx; ttwid=xxx; passport_csrf_token=xxx"
          }'
    ```

    > 若设置了 `DOUBAO_ADMIN_PASSWORD`，管理接口必须带 `X-Admin-Password` 头；
    > 未设置时容器内只能从回环访问，故请通过环境变量设置。

3. 验证连通性：

    ```bash
    curl -X POST http://127.0.0.1:10086/admin/api/checkin \
      -H "Content-Type: application/json" \
      -H "X-Admin-Password: $DOUBAO_ADMIN_PASSWORD" -d '{}'
    ```

> 未提供 `device_id` / `web_id` 时网关会生成随机占位值（上游可接受）。
> 若上游启用风控并返回 `gateway-error`，请在桌面端重新登录后重新导出 Cookie。

---

## 四、端点

### 4.1 推理接口

| 端点 | 方法 | 说明 |
| --- | --- | --- |
| `/v1/chat/completions` | POST | OpenAI Chat Completions，流式 / 非流式 |
| `/v1/messages` | POST | Anthropic Messages，流式 / 非流式 |
| `/v1/images/generations` | POST | OpenAI Images，文生图（返回 `url` / `b64_json`） |
| `/v1/videos/generations` | POST | 文生视频（返回 `url` / `vid` / 时长 / 分辨率） |
| `/v1/music/audio` | POST | 用音乐消息的 `video_id` 换取音频地址 |
| `/v1/models` | GET | 模型清单（含 `doubao.think_level` 等扩展字段） |

鉴权：`Authorization: Bearer <sk-...>` 或 `x-api-key`。
**未签发任何密钥时允许无密钥调用**（首次使用的便利）；一旦在控制台创建密钥即强制校验。

> **扩展字段**：聊天接口支持 `skill`（技能透传），可取 `image` / `music` / `video`
> 或直接传数值（3 / 9 / 17），对应上游 `option.action_bar_skill_id`；
> `input_skill` 对应上游 `ext.input_skill`（音乐技能入参）；
> `content` 里可放 `input_file` / `image_url` 分片（见下文「多模态」）。

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

浏览器直接打开 <http://127.0.0.1:10086/> 即为内置控制台单页（账号 / 密钥 / 日志 / 统计 / 设置）。
单文件内嵌、无外部依赖，调用的就是下面这些 JSON 接口：

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

## 五、配置

优先级：**命令行参数 > 环境变量 > 控制台设置 > 默认值**。

| 参数 | 环境变量 | 默认 | 说明 |
| --- | --- | --- | --- |
| `-host` | `DOUBAO_HOST` | `127.0.0.1` | 监听地址 |
| `-port` | `DOUBAO_PORT` | `10086` | 监听端口 |
| `-data` | `DOUBAO_DATA_PATH` | 可执行文件同目录 `doubao2api-data.json` | 状态文件 |
| `-doubao-dir` | `DOUBAO_DATA_DIR` | 自动探测 | DoubaoWork 数据目录 |
| `-admin-password` | `DOUBAO_ADMIN_PASSWORD` | 空 | 管理接口密码（非空时覆盖状态文件中的设置） |
| `-import` | — | — | 只导入一次后退出 |
| `-no-import` | — | — | 启动时不自动导入 |

控制台内还可调：模型别名、请求超时、重试次数、日志保留条数、CORS 来源。

### 5.1 状态文件与脱敏

所有状态（账号凭据、密钥、统计、日志）存在单文件里：

```text
doubao2api-data.json      # 含账号 Cookie / API 密钥
```

> ⚠️ **这个文件等同于账号密码。** 已写入 `.gitignore`，请勿提交、勿分享。

仓库提供结构相同、凭据全为占位值的样例：`doubao2api-data.example.json`。

### 5.2 CORS 与安全默认值

- 默认**只放行本机来源**（`localhost` / `127.0.0.1` / `::1`，任意端口），
  且**从不发送** `Access-Control-Allow-Credentials`。
- 需局域网访问时，在设置里显式填写逗号分隔的来源；`*` 通配符不生效。
- 默认监听 `127.0.0.1`，不暴露到局域网；需要时用 `-host 0.0.0.0` 显式放开。
- **管理接口**：未设密码时仅本机可访问，设置后必须带 `X-Admin-Password`；
  写操作做同源校验以阻断 CSRF。对外暴露（局域网 / 反向代理）时**务必设置
  管理密码**，否则控制台将不可达（而非裸奔）。
- 请求日志**只记元信息**（模型、耗时、长度），不落对话内容。

---

## 六、实现要点

### 6.1 零依赖地读 Chromium Cookie

Chromium 的 Cookie 加密链路有两层，且 Go 标准库没有 SQLite 驱动：

```text
Local State  ──[base64 → 去 "DPAPI" 前缀 → CryptUnprotectData]──▶ AES-256 主密钥
Cookies(SQLite) ──[b-tree 遍历 → 记录解码 → AES-256-GCM]──▶ 明文(含 32 字节域名前缀)
                                        └──[剥离 sha256(host_key)]──▶ 真实 Cookie
```

- **DPAPI** 用 `syscall.NewLazyDLL("crypt32.dll")` 直调 `CryptUnprotectData`。
- **SQLite** 用手写的极简只读解析器（`internal/doubao/sqlite.go`）：
  页头 / b-tree 遍历 / varint / serial type / 溢出页续链，仅够读 `cookies` 表。
- **AES-GCM** 用标准库 `crypto/aes` + `crypto/cipher`。

这样保持零第三方依赖，单文件约 10 MB。

### 6.2 补丁流还原为文本

上游不返回纯文本增量，而是一串 `content_block` 补丁。网关按块类型分派：

| 事件 / 块 | 处理 |
| --- | --- |
| `CHUNK_DELTA` `{"text":"..."}` | 紧凑增量，按当前阶段归入正文或思维链 |
| `block_type=10040` | 思考容器块；**第 1 个**之后的文本算思维链，**第 2 个**起算正文 |
| `block_type=10000` | `text_block.text` 正文 |
| `block_type=10101` / `10024` / `10025` | 加载提示 / 工具 / 联网搜索 |
| `patch_op[].patch_value.content` | JSON 字符串形式的新版增量 |
| `gateway-error` | 直接上抛（如会话过期、风控） |

> 踩过的坑：`option.answer_with_suggest` 置 `false` 会让上游**完全不返回思维链**，
> 必须为 `true`（代码中已注明）。

### 6.3 账号池与重试

- 轮询选号，优先选连续失败次数最少的账号；
- 全部不可用时回退到失败最少的那个，**不会硬阻塞请求**；
- 单次请求内失败自动换号重试（默认 3 次，有界）；
- 客户端断开（context 取消）时立即停止重试。

### 6.4 图片上传链路

上游只认自家资源中心的 `uri`，外链一律无效，因此网关内置了转存：

```text
POST /alice/resource/prepare_upload     →  STS 凭证 + service_id + upload_host
GET  /top/v1?Action=ApplyImageUpload    →  StoreUri / Auth / SessionKey（AWS SigV4 签名）
POST {UploadHost}/upload/v1/{StoreUri}  →  直传字节（Authorization + Content-CRC32）
POST /top/v1?Action=CommitImageUpload   →  最终 uri
```

踩过的坑：`FileExtension` 要带前导点、`UploadNum` 只在批量上传时出现、
`Content-CRC32` 是 **8 位十六进制**（不是十进制）、签名域为
`service=imagex` / `region=cn-north-1` / `path=/top/v1`。

### 6.5 文生图与技能触发

技能通过 `option.action_bar_skill_id` 传入，取值就是上游的 `SkillType`
（桌面端 bundle 枚举，与 `/samantha/skill/list` 的 `skill_type` 一致）：

| skill_type | 技能 | 单次调用可用？ |
| ---: | --- | --- |
| 3 | 图像生成 | ✅ 可用（免费额度），`default_prompt` = `生成一张图片:${style} ${content}` |
| 9 | 音乐生成 | ❌ 多步 + 配额门禁 |
| 17 | 视频生成 | ❌ 多步 + 配额门禁 |

图像生成**没有独立 REST 接口**（`/samantha/cozeplugin/txt2img` 实测 `no permission`），
真正链路是把 prompt 套进技能模板发一条对话，上游以 `block_type=2074` 的
`creation_block.creations[]` 回传（实测 2048×2048，Seedream）。
`/v1/images/generations` 与聊天接口的 `images[]` 都基于此。

> 实测：不带「生成一张图片:」前缀的裸提示词不会触发图像技能，模型会当普通问题回答。

### 6.6 文生视频：创意画布

视频生成**不在聊天技能链路上**。带 `skill=17` 会走「参数确认卡」并受会员配额限制
（实测 `STREAM_ERROR`：`710022004 rate limited`，`extra.decision` 来自 `shark_admin`），
但**创意画布执行接口**是单发的、可用的：

```text
POST /creativity/canvas/exec
  {exec_req:{request_id, operation:{canvas_type:50(GenVideo), generate_type:1(CREATE),
                                    prompt, use_model?, video_param:{duration, ratio}}},
   canvas_id_str:""}
  → data:{canvas_id, canvas_sub_id, task_status, result:{artifacts:[…]}}
```

该接口**按 `request_id` 幂等**：轮询就是原样重发；`task_status` 为
`1=PENDING / 2=RUNNING / 3=SUCCEEDED / 4=FAILED`，成功后取
`result.artifacts[0].video`：

```json
{"download_url":"https://aka.doubaocdn.com/s/…","vid":"v0369…",
 "duration":15.05,"width":1280,"height":720}
```

`use_model` 留空时上游自行选择（实测落到 `seedance_v2.0`）。实测一次生成约 3 分钟，
因此 `/v1/videos/generations` 单独把超时放宽到 15 分钟。

### 6.7 文生音乐：输入结构与音频解析已还原

- **输入结构**：音乐技能的入参放在 `ext.input_skill`（JSON 字符串）：
  `{"lyric":"…","theme":"…","mood":"…","genre":"…","gender":"","generation_type":"…"}`。
- **成品消息**：音乐以 `content_type=70`（LyricsToSongMusic）回传，其 `content_obj`
  携带 `video_id`；歌词是 `content_type=71`（另有 `72` = 多首成品）。
- **音频地址**：客户端用 `video_id` 调 `POST /alice/media/bigmusic/get_video`
  取真实地址（桌面端渲染器的 `fetchMusicAudioUrl`），**实测该接口可用**，
  返回带签名的 `video/tos/...` 地址。
- **歌词可单发**：`POST /alice/media/bigmusic/lyrics`（`{prompt}`，或
  `{prompt, genre, mood}` 走 `lyrics_web`）→ `data.lyrics_list`。
- **未闭环的部分**：`skill=9` 的聊天请求在这三个账号上**固定**返回
  `710022004 rate limited`（`shark_admin` 配额/风控），因此拿不到真实的
  `content_type=70` 样本。网关已实现 `input_skill` 透传与
  `/v1/music/audio`（`video_id` → 音频地址），有配额的账号可自行闭环。

### 6.8 扫码登录

`accounts.doubao.com` 的 passport SSO：

```text
GET  /passport/web/get_qrcode/?aid=582478&next=<url>   → 二维码(data URL) + token
POST /passport/web/check_qrconnect/?aid=582478&next=…   → status: new / scan / confirm
```

会话（含 Cookie jar）保存在内存，确认后跟随跳转拿 `sessionid` 等 Cookie 并入库。

---

## 七、项目结构

```text
doubao2api/
├── main.go                        # 入口：配置、启动、自动导入、优雅关闭
├── internal/
│   ├── doubao/
│   │   ├── types.go               # 账号 / 内容块枚举 / 档位常量 / 图片附件
│   │   ├── util.go                # UUID、指纹、Cookie 串解析
│   │   ├── sqlite.go              # 极简 SQLite 只读解析器（零依赖）
│   │   ├── cookie_windows.go      # DPAPI + AES-GCM + 域名前缀剥离 + 多 profile 导入
│   │   ├── cookie_other.go        # 非 Windows 平台占位
│   │   ├── sse.go                 # SSE 事件流解析
│   │   ├── upload.go              # 图片转存：prepare_upload + imageX 直传（SigV4）
│   │   ├── video.go               # 文生视频：创意画布 exec + 幂等轮询
│   │   ├── music.go               # 音乐：input_skill 结构 + get_video 音频解析
│   │   └── client.go              # 上游客户端：公参、请求体、补丁流解析
│   ├── store/store.go             # 状态持久化、账号池、密钥、统计、日志
│   ├── registry/registry.go       # 模型清单与别名解析
│   └── server/
│       ├── server.go              # 路由、鉴权、CORS、控制台 API
│       ├── chat.go                # /v1/chat/completions、多轮、重试、附件转存
│       ├── anthropic.go           # /v1/messages 双向转换
│       ├── images.go              # /v1/images/generations（文生图技能）
│       ├── videos.go              # /v1/videos/generations（创意画布 GenVideo）
│       ├── music.go               # /v1/music/audio（video_id → 音频地址）
│       ├── login.go               # 扫码登录（passport SSO）
│       ├── web.go                 # 控制台单页的内嵌与响应头
│       └── web/index.html         # 控制台单页（单文件，无外部依赖）
├── cmd/probe/                     # 上游探测工具（逆向排障用，非网关功能）
├── tools/extract_pak.py           # biz.pak 解包脚本（第一步，可重跑）
├── tools/extract_sources.py       # source map → 源码还原脚本（第二步，可重跑）
├── doubao2api-data.example.json   # 脱敏状态文件样例
├── .gitignore
├── go.mod                         # 无 require，纯标准库
└── README.md
```

---

## 八、当前进度与后续

### 已完成（第一阶段：精简网关）

- [x] 桌面端凭据自动导入（DPAPI + AES-GCM + 域名前缀），支持多 profile 批量导入
- [x] 上游协议复刻（公参、请求体、SSE 补丁流）
- [x] `/v1/chat/completions`（流式 / 非流式）+ `/v1/models`
- [x] 三档模式与思维链 → `reasoning_content`
- [x] 多轮会话（压平历史 + `conversation_id` 原生续接）
- [x] 账号池轮询与有界重试
- [x] 控制台 API（账号 / 密钥 / 日志 / 统计 / 设置），带管理鉴权与 CSRF 防护
- [x] Docker 一键部署（多阶段构建、非 root、健康检查）
- [x] 控制台单页（内嵌 HTML 面板，无外部依赖）
- [x] Anthropic `/v1/messages` 双向转换（含 `thinking` 块与图片）
- [x] 图片理解：`block_type=10052` 上传链路（`/alice/resource/prepare_upload` + imageX 直传）
- [x] 文件解析（PDF / Office / 文本，附件 `type=3`）
- [x] 文生图（`/v1/images/generations`，基于图像生成技能 `skill_type=3`）
- [x] 文生视频（`/v1/videos/generations`，基于创意画布 `canvas_type=50`，已实测）
- [x] 扫码登录（passport SSO，`/admin/api/login/qrcode`）
- [x] 技能透传（聊天接口 `skill` / `input_skill` 字段）
- [x] 音乐音频解析（`/v1/music/audio`，`video_id` → 音频地址，已实测）

### 后续阶段

- [ ] 文生音乐成品：输入结构（`ext.input_skill`）与成品消息（`content_type=70`）
      均已还原，但上游对音乐技能固定 `710022004 rate limited`，
      当前账号无配额，拿不到真实成品样本（见 6.7）

---

## 九、注意事项

- **本项目仅供本机 / 内网自用与协议研究。** 上游服务的使用受豆包服务条款约束，
  请勿公开暴露到公网或用于商业转售。
- 凭据即账号。`doubao2api-data.json` 里的 Cookie 等同于账号密码，
  **不要提交到仓库或分享**。
- 逆向产物 `.recon/`（`biz.pak` 解包结果 `extract/` 约 580 MB、source map
  还原的第三方源码 `src/` 约 16 MB）体积大且含第三方版权内容，已加入 `.gitignore`，
  仅作本地协议比对；两者都可由 `tools/` 下两个脚本重新生成，不入库也不影响构建。
- 上游可能随时调整协议或启用风控。若出现 `gateway-error`，
  请重新登录桌面端后重新导入账号。

---

## 十、许可证

本项目以 [Apache License 2.0](LICENSE) 授权发布。

```text
Copyright 2026 Lizi

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
```

许可证覆盖的是**本项目自身的代码**。逆向过程中接触到的第三方内容
（豆包客户端二进制、`biz.pak` 解包产物、source map 还原出的源码）
不包含在本仓库内，其权利仍归原权利人。

> 许可与使用限制是两件事：Apache-2.0 授予你使用、修改、分发本代码的权利，
> 但并不豁免你对上游服务条款的义务。请遵守第九节的注意事项。
