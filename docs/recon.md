# 逆向结论与实现要点

> 面向想了解「为什么这么实现」的读者。日常使用见 [README](../README.md)，
> 接口契约见 [api.md](api.md)，安全模型见 [security.md](security.md)。

## 逆向结论：链路很短，但凭据加密有两层

DoubaoWork 是**基于 Lark/飞书内核的 Chromium 原生应用**（不是 Electron）：
主逻辑打包在 `local_webcontents/biz/biz.pak`（147 版本约 195 MB，旧版 224 MB 自定义包），
AI 能力由内部代号 **`samantha`** 的服务提供，账号资料走 **`alice`**。

推理入口本身是一个标准的 SSE 接口，**不需要伪造签名、不需要设备指纹算法、
不需要驱动桌面客户端**——只要能拿到那份 Cookie。

| 环节 | 逆向结果 |
| --- | --- |
| 应用形态 | Chromium 原生应用（Lark 内核），`biz.pak` = 12 字节头 + 6 字节索引 + N 个 blob |
| 业务代号 | `samantha`（AI 会话）、`alice`（账号 / 资料） |
| **推理入口** | `POST https://www.doubao.com/chat/completion` |
| **鉴权** | Cookie（`sessionid` / `sid_guard` / `ttwid` / `odin_tt`）+ 头 `x-tt-passport-csrf-token`（可经 `/passport/safe/csrf_token/` 刷新）+ `Agw-Js-Conv: str` |
| 公参 | `aid=1044603`、`device_platform=web`、`doubao_device_platform=desktop`、`pc_version=2.36.15`、`runtime_version=3.45.5`、`channel=win`、`fp=verify_<device_id>`、`iid`、`web_id`、`web_tab_id` |
| 客户端版本 | DoubaoWork `147.0.7727.149` / 应用 `2.36.15` / 前端包 `3.45.5` / `VERSION_CODE=20800` |
| 请求体 | `client_meta` + `messages[].content_block` + `option` + `ext`（JSON 明文） |
| 响应 | SSE：`SSE_ACK` → `STREAM_MSG_NOTIFY` → `STREAM_CHUNK` / `CHUNK_DELTA` → `SSE_REPLY_END` |
| 凭据落盘 | `<User Data>\<profile>\Network\Cookies`（**SQLite**） |
| 凭据加密 | **两层**：DPAPI 保护的主密钥（`Local State`）+ AES-256-GCM 逐条加密 |
| 域名绑定 | Chrome 130+ 起，Cookie 明文前置 32 字节 `sha256(host_key)`，解密后须剥离 |

### 1.1 `biz.pak` 包格式与还原流程

```text
header : version(u32 LE) | reserved(u32) | field2(u32)
index  : N × { id(u16 LE) | offset(u32 LE) }   // 按 offset 升序
v5     : index 以 { id=0, offset=文件长度 } 哨兵结束，后面还有约 3 KB 元数据
         才到第一个 blob；field2 不再是条目数
data   : 第 i 个 blob = [offset_i, offset_{i+1})
```

本机实测（147.0.7727.149）：`version=5`、9810 个 blob、源包 195 MB、解压后 525 MB。
`tools/extract_pak.py` 已兼容新旧两种布局：旧版信任 header 的条目数，v5 用
「哨兵 / 首个 offset」推断 index 长度。

> **注意**：当前 147 构建**不再随包携带 source map**（还原脚本统计为 0 份）。
> 因此本项目的协议结论来自：上一版还原出的 3010 个 TS 源文件、当前 bundle 中
> 未被混淆的枚举 / 端点 / 请求体字符串，以及对上游的实测探针。

还原分两步（均可随时重跑）：

```bash
# 1) 拆包：把 pak 拆成一堆 blob（产物勿入库）
python tools/extract_pak.py "F:\IDE\DoubaoWork\app\local_webcontents\biz\biz.pak" .recon/extract

# 2) 还原源码：解出 source map 的 sourcesContent，按原目录结构落盘
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
| 上游入口与请求头 | `/samantha/chat/completion`、`Agw-Js-Conv: str`、`tz_name` |
| 请求体构造 | `L1-Arch/business/api/src/api/samantha_api`、`im-sdk/protocol`（`content_block`） |
| 公参定义 | `src/init/params/index.ts` + `get-common-params`：`aid=1044603`、`doubao_device_platform=desktop`、`fp=verify_<device_id>` |
| 应用常量 | `PROJECT_VERSION=3.45.5`、`VERSION_CODE=20800`、`DESKTOP_DOUBAO_WORK_APP_ID=1044603` |
| CSRF 刷新 | `GET /passport/safe/csrf_token/`（必须去掉 `real_aid`） |
| 内容块枚举 | `10000` 文本、`10040` 思考、`10024` 工具、`10025` 联网搜索、`10052` 附件、`2074` 生成结果；另见 6.2 的完整扩展表 |
| 技能枚举 | `0` Coding、`2` 写作、`3` 图像、`4` 搜索、`5` 翻译、`6` 阅读、`9` 音乐、`17` 视频、`18` PPT …… |
| 消息内容类型 | `70` 音乐成品、`71` 歌词、`72` 多首成品、`2021` 视频、`2022` 多产物、`2073` 搜索引擎 |

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

## 实现要点

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
| `10101` / `10024` / `10053` | 加载提示 / 工具 / 提示 → 工具信息 |
| `10005` / `10025` / `10050` | 联网搜索 → `sources[]`（标题 / URL / 摘要 / 站点） |
| `10008` | 代码块 → 完成时以 Markdown 代码围栏输出一次 |
| `10010` | 生图过程块 → `images[]` |
| `10011` / `10013` / `10014` / `10027` | 深度研究 / 网页 / 状态 / 图片理解 → 工具信息 |
| `10019` / `10020` / `10021` | 文件操作 / 文件 / 代码产物 |
| `10030` / `10058` | 产物块 → `artifacts[]`（resource_id / resource_type / 标题） |
| `10051` / `10056` | 链接 / 引用 → `sources[]` |
| `10075` | 音乐成品 → `music[]`（video_id / 标题 / 时长） |
| `2074` | 生成结果 → `images[]`（`creation_block.creations[]`） |
| `patch_op[].patch_value.content` | JSON 字符串形式的新版增量 |
| `gateway-error` | 直接上抛（如会话过期、风控） |

> 踩过的坑：`option.answer_with_suggest` 置 `false` 会让上游**完全不返回思维链**，
> 必须为 `true`（代码中已注明）；新版客户端新增的 `conversation_init_ext` /
> `model_config` / `aggregate_params` 不能带空 `model_item_key`，否则返回
> `710020202 common invalid param`。

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

### 6.7 文生音乐：歌词 / 成品 / 音频

- **歌词可单发**：`POST /alice/media/bigmusic/lyrics_web`（`{prompt, genre, mood}`）或
  `POST /alice/media/bigmusic/lyrics`（`{prompt}`）→ `data.lyrics_list`。
  网关已封装为 `POST /v1/music/lyrics`（实测可用）。
- **输入结构**：音乐技能的入参放在 `ext.input_skill`（JSON 字符串）：
  `{"lyric":"…","theme":"…","mood":"…","genre":"…","gender":"","generation_type":"…"}`。
- **成品消息**：音乐以 `block_type=10075`（`lyrics_to_songs_music_block`）回传，
  其 `lyrics_to_songs_music_content` 携带 `video_id` / 标题 / 封面；
  聊天响应会把它归入 `music[]`。
- **音频地址**：客户端用 `video_id` 调 `POST /alice/media/bigmusic/get_video`
  取真实地址（桌面端渲染器的 `fetchMusicAudioUrl`），**实测该接口可用**，
  返回带签名的 `video/tos/...` 地址。
- **未闭环的部分**：`skill=9` 的聊天请求在当前账号上仍可能返回
  `710022004 rate limited`（`shark_admin` 配额/风控）；有配额的账号可直接把
  聊天流里的 `music[].video_id` 交给 `/v1/music/audio` 闭环。

### 6.8 扫码登录

`accounts.doubao.com` 的 passport SSO：

```text
GET  /passport/web/get_qrcode/?aid=582478&next=<url>   → 二维码(data URL) + token
POST /passport/web/check_qrconnect/?aid=582478&next=…   → status: new / scan / confirm
```

会话（含 Cookie jar）保存在内存，确认后跟随跳转拿 `sessionid` 等 Cookie 并入库。

### 6.9 请求头、公参与 CSRF 刷新

聊天接口实际发送的关键头（与桌面端 3.45.5 对齐）：

```text
Content-Type: application/json
Accept: text/event-stream
User-Agent: Mozilla/5.0 … Chrome/147.0.7727.149 Safari/537.36 SamanthaDoubaoWork/2.36.15
Origin / Referer: https://www.doubao.com
Accept-Language: zh-CN,zh;q=0.9
Agw-Js-Conv: str                 # int64 → 字符串，避免 conversation_id 丢精度
x-tt-passport-csrf-token: <token>
Cookie: <导入的 Cookie>
```

公参关键值：`aid=1044603`、`real_aid=1044603`、`device_platform=web`、
`doubao_device_platform=desktop`、`web_platform=desktop`、`pkg_type=release_version`、
`version_code=20800`、`pc_version/doubao_pc_version=2.36.15`、`runtime_version=3.45.5`、
`chromium_version=147.0.7727.149`、`client_platform=pc_client`、`channel=win`、
`fp=verify_<device_id>`、`iid`、`web_id`、`tea_uuid`、`web_tab_id`、`tz_name`（聊天）。

CSRF token 的自动刷新：

```text
GET /passport/safe/csrf_token/?aid=…&device_id=…
  → {"data":{"passport_csrf_token":"…"},"message":"success"}
```

> 这个接口**必须去掉 `real_aid`**（带上会返回 `error_code=22 非法应用`）。
> 网关缓存 token 30 分钟；缺失时预刷新，聊天被登录 / CSRF 类错误拒绝且
> 尚未输出内容时强制刷新并重试一次。

### 6.10 新版客户端参数与「技能模型」

- 公参新增 `doubao_device_platform` / `web_platform` / `channel` / `iid` / `doubao_pc_version`；
  `device_platform` 仍兼容为 `web`。
- `fp` 固定为 `verify_<device_id>`（不再是随机串）；`web_tab_id` 每账号固定。
- `option` 新增 `agent_mode` / `is_from_click_option` 等；
  模型选择走 `conversation_init_ext` / `model_config` / `aggregate_params`（不传空键）。
  > 客户端默认开启的 `support_lazy_fetch_stream` 需要配套的惰性拉流实现，
  > 网关暂不开启，避免长回复 / 产物场景丢包。
- `GET /v1/skills` 拉取 `/samantha/skill/list`，并给出技能 → 模型映射；
  技能模型在发送前自动套用 `default_prompt`。
- `GET /v1/conversations` → `/alice/conversation/list`；
  `PATCH/DELETE/POST .../pin|unpin|archive|clear` → `/alice/conversation/*`。

### 6.0 2026-11 重新取证（147.0.7727.149）

对当前安装的客户端重新取证，纠正/补充了若干结论。

**取证路径变了**：147 版的 `biz.pak` 里只剩编译产物（JSON 元数据 + V8 code cache +
ReactLynx 快照 + 一份 wasm），**不再有可读 JS**，也仍然没有 source map。
真正的可读 JS 在另外两处：

| 位置 | 内容 | 规模 |
| --- | --- | --- |
| `%LOCALAPPDATA%/DoubaoWork/User Data/gecko_cache/<id>/flow_web_entries/.../static/js` | 豆包 Web 端 bundle（SSE 解析、会话、技能都在这里） | 254 个 JS，最大 2.4 MB |
| `<安装目录>/app/local_webcontents/apps/{entry-main,entry-moa,doubao-office}/main.js` | 桌面端容器层（公参、请求头、特性开关、本地工具） | 665 KB / 2.8 MB / 1.3 MB |

命令：`python scripts/recon_pak_probe.py <pak> <out>` 按关键字定位 pak 内 blob；
`python scripts/recon_ctx.py <bundle> <keyword>` 打印 minified JS 的可读切片。

#### 公参（逐字，`entry-moa/main.js`）

```js
{ version_code: getParam("version_code"),                 // 20800
  ...(hasParam("update_version_code") ? { update_version_code: ... } : {}),  // 新增（可选）
  language, device_platform: runtime ? "web" : device_platform,
  doubao_device_platform: device_platform,                 // desktop
  aid, real_aid: aid, pkg_type, device_id,
  pc_version, doubao_pc_version: pc_version,
  web_id, tea_uuid: uuid,                                  // ← 独立 uuid，不是 device_id
  region, sys_region: region, samantha_web: 1,
  web_platform, "use-olympus-account": 1,
  runtime, runtime_version, client_platform: "pc_client",
  chromium_version, channel, fp: `verify_${device_id}` }
```

与网关 `SecurityParams` 的差异：网关**多**发 `iid` / `web_tab_id` / `msToken`
（客户端公参里没有，服务端兼容），**少**发 `update_version_code`（本地无法取到该值，
未发；服务端也不强制）。`tea_uuid` 网关沿用 `device_id`（客户端用一次性 uuid），
实测不影响。

#### 请求头（`createFetchStream` 调用处）

```js
headers: [["content-type","application/json"], ["Agw-Js-Conv","str"], ...getCommonHeaders()]
```

`getCommonHeaders` 在懒加载 chunk（模块 593234）里，未落在本地缓存；从调用点可确认
聊天请求只有上面两个固定头 + 公共头（csrf / UA 等）。WebSocket 路径另有一套签名：
`X-Bogus` / `X-MS-STUB`（`bdms.frontierSign`），**SSE 路径不需要**。

#### 聊天入口

Web 端走 `POST /samantha/chat/completion`；网关用 `POST /chat/completion`
（同样被接受，生产已验证）。两条路径都在，保留网关现有实现。

#### 模型列表

客户端**没有静态模型清单**：模型相关能力全部来自服务端配置
（特性开关 `moa_config.model_name`、`moa_config.enable_switch_model`、
`moa_config.enable_new_choice`）。因此 `/v1/models` 里的「快速 / 思考 / 专家 + 技能」
是**网关自己的抽象**，不是上游枚举 —— 文档里必须说清，避免误导。

#### 工具调用

当前客户端的"工具调用"是**本地沙箱工具**，结果回传上游：

| 端点 | 关键字段 |
| --- | --- |
| `POST /alice/office/tool_local/upload_tool_call_result` | `sandbox_id` / `tool_result` / `device_id` |
| `POST /alice/office/tool_local/upload_tool_call_result_v2` | 同上 |
| `POST /alice/office/tool_local/upload_pre_build_sandbox_result` | 见 bundle |

工具在客户端沙箱里执行，网关侧无法复现（没有沙箱环境）。上游返回的
`block_type=10024`（通用工具块）网关已作为工具提示透传，行为保持不变。

#### 已失效的旧结论

- `need_deep_think`、`action_bar_skill_id`、`content_block` 在当前 147 客户端
  **已不再出现**（0 命中）。网关仍发送这些字段：服务端继续兼容（思考档位、技能均实测有效），
  但不要再把它们当作"当前客户端协议"引用。
- 深度思考现在是 UI 特性开关（`deep_think_show` / `deep_think_memory`），
  请求侧档位由服务端配置决定。
- SSE 解析（`block_type` / `content_block` / `is_finish`）在 Web 端 bundle 里，
  桌面容器层不含这些字段。

#### 工作 / 智能体模式（未完成，已回滚实现）

参考项目 README 给出了工作模式的实测字段表与可用模型清单，摘录如下（供后续对齐）：

| 字段 | 值 |
| --- | --- |
| `option.agent_mode` | `1` |
| `option.conversation_mode` | `2` |
| `option.mode_id` / `option.mode_name` | `"3"` / `工作任务` |
| `option.model_config` / `aggregate_params` / `conversation_init_ext` 的 `model_item_key` | 三处必须一致 |
| `option.conversation_init_ext.reasoning_effort` | `"0"`–`"4"` |
| `option.sse_recv_event_options.support_chunk_delta` | `true` |
| `enable_sandbox` | headless 场景必须 `false`（沙箱在客户端本地） |

工作模式可用模型（`model_item_key`，参考项目逐个实测）：

| 对外名 | model_item_key | 备注 |
| --- | --- | --- |
| `auto` | `9` | 免费 |
| `doubao-2.1-lite` | `seed-lite-7b` | 256k |
| `doubao-2.1-pro` | `5` | 256k |
| `deepseek-v4-flash` | `person-deepseek-v4-1-flash-261017` | **默认** |
| `deepseek-v4-pro` | `person-deepseek-v4-pro-ga-260813` | |
| `kimi-k3` | `person-kimi-k3-261111` | |
| `kimi-k3.1-pro` | `person-kimi-k3-1-pro-261111` | |
| `kimi-k3.1-pro-fast` | `person-kimi-k3-1-pro-highspeed-261111` | |
| `glm-5.3` / `glm-5.3-flash` | `person-glm-5-3-261111` / `…-flash-261111` | 上游报错话术，疑未开通 |

> 清单来源是**客户端缓存** `_DesktopActionBarBriefListRequestCacheDataV1` 里的
> `modeSelectData.menuConfV2.mode_list`，不是接口下发 —— 这也解释了为什么
> `/v1/models` 里没有 DeepSeek / Kimi：它们是**工作模式**的模型，普通对话不存在这些型号。

**本项目的尝试与结论**：按上述字段（含 `agent_mode=1`、`conversation_mode=2`、
`mode_id`、三处一致的 `model_item_key`、`enable_sandbox=false`、最小 option 字段集）
构造请求，上游仍返回 `710020202 common invalid param`；试过 6 轮组合均失败，
已**回滚**工作模式实现，避免留下"看似支持、实际不可用"的路径。

#### 真正的阻塞点：沙箱实例必须由上游预创建（已从 bundle 取证）

在 `entry-moa/main.js`（@1934394）找到客户端的 sandbox client option 构造：

```js
let d = {
  client_env_id:     a,                 // 来自沙箱创建结果
  enable_sandbox:    true,
  os:                e.detectClientOs(),
  ...(c ? { preinstall_skill_path: c } : {}),
  sandbox_auth_type: o,
  sandbox_env_id:    r.envId,           // ← 上游下发
  sandbox_id:        i.instanceId,      // ← 上游下发
  shared_folder_path: i.resolvedSharedFolders,
  workspace:          l,
};
// 随后 clientOptionJson: JSON.stringify(d)
```

同一处还有 `create_sandbox_instance_failed` / `preBuildSandboxKey` / `instanceId`，
说明客户端是**先调沙箱创建接口**拿到 `instanceId` 与 `envId`，再把它们塞进聊天请求。
我们用 `sandbox-<uuid>` 自造这两个 id，正是被 `710020202 common invalid param` 拒绝的原因。

由此可以下一个明确结论：

- **工作模式的工具调用无法在 headless 网关里复现** —— 工具在客户端本机沙箱执行
  （`computer_use_tool` / `cua` / `preinstall_skill_path`），网关既没有沙箱运行时，
  也拿不到合法的沙箱实例 id。
- 字段名层面还有一处需要注意：客户端用的是 `client_env_id` / `sandbox_env_id`
  这类键，而参考项目的 `agent_task_param` / `task_input_json` / `conversation_mode`
  在当前 147 bundle 里 **0 命中** —— 那些是它按旧版或自行重构的写法，不能照抄。

#### 结论修正：工作模式「不用沙箱」就能跑通（已实测）

上面的沙箱结论只解释**工具调用**为什么做不到，并不代表工作模式本身不能用。
进一步实测发现：**只要不下发沙箱相关字段，工作模式会话完全可用**。

构造方式（`agent_mode>0` 分支，见 `internal/doubao/client.go`）：

```jsonc
option: {
  agent_mode: 1,
  conversation_mode: 2,
  mode_id: "3",
  need_deep_think: 10001,          // 工作模式固定值，普通对话是 0/1/3
  conversation_init_ext: { model_item_key, reasoning_effort: "4" },
  model_config:          { model_item_key },
  aggregate_params:      { model_item_key }
}
ext: { use_deep_think: "10001" }
```

关键约束（踩过的坑）：

- `model_item_key` 必须在上面三处**一致**，且不能为空；
- `reasoning_effort` **只在 `conversation_init_ext` 里下发**（放进 `model_config` /
  `aggregate_params` 会被判 `710020202 common invalid param`）；
- 不要带 `mode_name`（同样会触发 invalid param）；
- 沙箱相关字段（`client_option` / `sandbox_id` / `sandbox_env_id` / `general_task_param`）
  **一律不要下发**；一旦带上，就必须是上游签发过的实例 id。

实测：`/v1/models` 已包含 5 个工作模式模型（`deepseek-v4-flash` / `-pro`、
`kimi-k3`、`kimi-k3.1-pro`、`doubao-2.1-pro`），经网关调用返回 200 +
`conversation_id`；`kimi-k3` 流式验证通过（4 个 chunk、正常 `[DONE]`、
回答 `Hello! Nice to meet you.`）。

仍缺的部分是**工具调用**：上游把工具调用放在 `/alice/office/tool_local/msg/fetch`
长轮询里（不是 SSE），需要网关实现"建沙箱 → 拉取工具调用 → 本地执行 → 上传结果"
的完整回路，其中沙箱实例 id 必须由上游预建（`pre_build_sandbox_key` →
`upload_prebuild_sandbox_result`）。这属于新增运行时，未实现。

> 若真要实现，注意安全边界：模型可以要求在"沙箱"里执行任意命令/读写文件，
> 网关侧实现等于把 RCE 暴露给模型输出，必须配合白名单/容器隔离，不能直接跑在本机。

### 6.11 积分、签到与任务奖励

豆包有「积分（credits）」和「额度（quota）」两个概念：

- `GET /v1/credits`：调用 `/commerce/benefit_supply/credit/get_credit_num_optional_tasks`，
  返回 `credit.total_credit_num`、每日登录任务与积分规则链接。
- `POST /v1/checkin`：**2026-11 起上游已废弃** `/alice/ug/campaign/landing/checkin`
  （任意 body 都返回 `710010702 system error`）。现在签到表现为活动里的
  `daily_signin` 任务（`task_kind=daily_signin`、`reward_mode=2` 直接领取）：
  读取活动详情 → `POST /alice/ug/event/report`
  （`scene=campaign_task_click`）创建任务实例 → `POST /alice/ug/campaign/item/reward/claim/`
  领取。旧接口仅作为回退保留。
  签到的权威状态在任务详情的 `daily_checkin` 里：
  `checked_in_today` / `continuous_days` / `last_checkin_date` / `reward`。
  因此 `/v1/tasks` 会直接带出 `daily_checkin`，可据此判断今天是否已签到。
- `GET /v1/tasks`：调用 `/alice/ug/campaign/v3/detail/`，默认活动组为
  `pc_incentive_task_mvp_v3`；返回任务 / 里程碑、状态、奖励（`quota_grant`）与进度。
- `POST /v1/tasks/claim`：调用 `/alice/ug/campaign/item/reward/claim/`，
  参数 `task_id` / `campaign_group_code` / `campaign_item_code`。
- `POST /v1/tasks/claim-completed`：先读任务详情，只领取 `task_status=completed`
  或 `reward_mode=direct_claim` 且未领取的奖励；**不会伪造任务完成行为**。

任务状态：`1=created`、`2=completed`、`3=rewarded`、`4=in_progress`；
领取模式：`1=complete_then_claim`、`2=direct_claim`。

领取响应里的 `data.biz_error.code` 有三类语义（客户端同款）：

| biz_code | 含义 | 网关映射 |
| ---: | --- | --- |
| `0` / `4` / `6` | 成功（4、6 是客户端认可的成功码） | `claimed` |
| `3` | **领取结果确认中，请稍后重试**（异步发放） | `pending`（短暂重试 3 次后仍为 3 就如实回报） |
| 其它 | 真失败 | `failed` |

#### 待确认签到的后台重试队列

上游把签到奖励做成异步发放，因此 `/v1/checkin` 可能返回 `pending`。
网关把这情况登记进状态文件（`pending_checkin_date` / `pending_checkin_at` /
`pending_checkin_tries`），并在开启 `retry_pending_checkin` 后每小时复查一次：

| 处置 | 条件 |
| --- | --- |
| 重试 | 记录是今天的，且重试次数 < 6 |
| 作废 | 记录跨天（签到按天幂等，昨天的 pending 已无意义） |
| 放弃 | 今日已重试 6 次；记录保留，控制台可见 |

复查成功（`checked_in && !pending`）会把账号补成「已签到」并写入奖励额度，
之后 `/v1/tasks` 的 `daily_checkin.checked_in_today` 即为 `true`。
也可以手动催一次：`POST /admin/api/checkin/retry`（仍受开关约束）。

> **已知上游限制（2026-11-14 实测）**：`daily_signin` 任务能创建实例
> （`task_status=4 in_progress`）、能提交领取，但领取恒返回 `biz_code=3`，
> 且 `daily_checkin.checked_in_today` 始终为 `false` —— 说明**真正的签到动作
> 上游没有再执行**（旧接口 `landing/checkin` 就是那个动作）。网关无法伪造该动作，
> 因此 `/v1/checkin` 会如实返回 `pending=true`；等上游恢复后无需改代码即可生效。

自动执行（默认关闭）：

```bash
# 开启后台自动签到 + 自动领取 + 自动尝试做任务
curl -X POST http://127.0.0.1:10086/admin/api/settings -H 'Content-Type: application/json' -d '{"auto_checkin":true,"auto_claim_tasks":true,"auto_do_tasks":true,"auto_task_limit":3}'
```

后台每 30 分钟检查一次：

| 设置 | 行为 |
| --- | --- |
| `auto_checkin` | 每天每账号签到一次（按 `last_checkin_date` 去重） |
| `auto_claim_tasks` | 只领取 `completed` / `direct_claim` 且尚未领取的奖励 |
| `auto_do_tasks` | 每天每账号最多尝试 `auto_task_limit` 个任务 |

自动做任务的流程（`/v1/tasks/run` 与后台一致）：

1. `POST /alice/ug/event/report`（`scene=campaign_task_click`）创建任务实例；
2. 取任务 `recommend_prompts` 的 `prompt`，以 `agent_mode=4`（WorkTask）发送对话；
3. 轮询 `/alice/ug/campaign/v3/detail/` 直到 `completed` / `rewarded`；
4. 领取奖励；
5. 清空并归档本次会话，清理 `doubao2api-*` 临时文件（1 小时前）。

> 上游部分任务依赖客户端专用 WorkTask 技能（`lark-doc` / `lark-sheets` /
> `Lark-sLides-pro` / `schedule`）。网关会尝试发送推荐提示词并如实返回
> `claimed` / `not_completed` / `skipped` / `failed`，**不会伪造任务完成**。
> 当前账号实测：自动签到与领取可用；任务尝试可能因上游技能链返回 `not_completed`。
> `skill_use`、`work_buddy_add_chat`、`enable_notification` 等需要真实产品动作的任务
> 不在自动执行范围内。

---

### 6.12 与参考项目 yyydsz/doubao2api 的协议对照

对照 [yyydsz/doubao2api](https://github.com/yyydsz/doubao2api)（Go，约 3.2k 行，
`internal/doubao` + `internal/config` 内置默认公参）。结论：**协议本体一致，
无需按它更新**；差异集中在「产品线 / 客户端版本 / 功能范围」。

| 维度 | 本项目（DoubaoWork 147） | 参考项目 | 结论 |
| --- | --- | --- | --- |
| 推理入口 | `POST /chat/completion` | 同 | 一致 |
| `aid` / `real_aid` | `1044603`（工作端） | `582478`（主端，`local_app_id` 同） | 产品线不同，不改 |
| `device_platform` | `web` + `doubao_device_platform=desktop` | `desktop` | 我们从 147 bundle 取证（`runtime ? "web" : …`），不改 |
| 版本号 | `pc_version=2.36.15` / `runtime_version=3.45.5` | `2.30.1` / `3.37.4` | 他们对应更早的客户端 |
| `chromium_version` / `version_code` | `147.0.7727.149` / `20800` | 同 | 一致 |
| 请求头 | `Content-Type` + `Agw-Js-Conv: str` + `Accept: text/event-stream` + UA/Origin/Referer/Cookie/CSRF | 另加 `agw-js-conv: str, str`、`last-event-id: undefined`、`x-flow-trace: 04-…-01`、`Accept: */*` | 我们取自当前 bundle（`["Agw-Js-Conv","str"]`）；两边实测都能跑，不改 |
| `need_deep_think` | `0/1/3`（快速/思考/专家，三档可映射） | `10001`（`ext.use_deep_think` 同值） | 实测两者上游都接受（`think=10001` 正常返回），保留我们的三档语义 |
| `answer_with_suggest` | `true`（实测置 false 会丢思维链） | `false`（该项目不做思维链透出） | 保留 |
| 请求体骨架 | `client_meta` + `messages[].content_block` + `option` + `ext` | 同 | 一致 |
| 额外可选字段 | 无 | `client_meta.local_permissions`、`option.support_lazy_fetch_stream` / `is_old_user` / `message_storage_type` / `conversation_mode`、`user_context: []` | 未验证有收益，不盲加 |
| Work / Agent 模式 | 未实现 | `agent_mode` + `general_task_param`（sandbox_id / workspace / `task_input_json` / `client_option`） | **功能缺口**，对应 roadmap 的「工具调用 / 异步长任务」 |
| SSE 解析 | 块类型注册表，30 种 `block_type` | 仅 `10000` 文本 / `10040` 思考标题 | 我们更完整 |
| 签到 | `daily_signin` 任务 + `pending` 语义 + 每小时复查队列 | 同一个 `landing/checkin`，把 `710010702` 映射成 `no_campaign` | 我们更完整；`scene` 填活动组码同样报错（已实测） |

值得后续跟进的两点（都属新增能力，不是修 bug）：

1. **`support_lazy_fetch_stream`**：参考项目在 `option` 里置 `true`，可能影响长回复的拉取方式；
   可在一次长对话里 A/B 验证后再决定。
2. **`a_bogus` 可选签名**：参考项目预留了该项（配置注入），默认不启用；
   若哪天遇到风控类拒绝，可作为兜底手段。

## 项目结构

> 以下为当前实际布局（`internal/` 下按职责拆分，单个文件不再超过 ~700 行）。

```text
doubao2api/
├── main.go                        # 入口：配置、启动、自动导入、优雅关闭
├── internal/
│   ├── account/account.go         # 账号模型：持久化字段 + 运行时状态（CSRF / tab id）
│   ├── ids/ids.go                 # UUID / 十六进制 / 数字串（无依赖的叶子包）
│   ├── doubao/                    # 上游协议层
│   │   ├── types.go               # 账号常量重导出 / 内容块枚举 / 技能枚举 / 档位
│   │   ├── util.go                # Cookie 串解析、Local Storage 正则、随机 ID 薄封装
│   │   ├── sqlite.go              # 极简 SQLite 只读解析器（零依赖）
│   │   ├── cookie_windows.go      # DPAPI + AES-GCM + 域名前缀剥离 + 多 profile 导入
│   │   ├── cookie_other.go        # 非 Windows 平台占位
│   │   ├── csrf.go                # CSRF token 刷新与重试判定
│   │   ├── sse.go                 # SSE 事件流解析（多行 data / 注释 / 心跳）
│   │   ├── stream.go              # 事件信封 → 增量：SSE_ACK / CHUNK_DELTA / patch_op
│   │   ├── stream_blocks.go       # 块类型注册表（30 种 block_type 的分派）
│   │   ├── blocks.go              # 块内容解析（搜索来源 / 代码 / 产物 / 音乐）
│   │   ├── skills.go              # 技能元数据 + /samantha/skill/list + postJSON
│   │   ├── rewards.go             # 积分 / 签到 / 激励任务
│   │   ├── endpoints.go           # 会话 / 歌词等 JSON 端点
│   │   ├── upload.go              # 图片转存：prepare_upload + imageX 直传（SigV4）
│   │   ├── video.go               # 文生视频：创意画布 exec + 幂等轮询
│   │   ├── music.go               # 音乐：input_skill 结构 + get_video 音频解析
│   │   └── client.go              # 公参、请求头、请求体构造
│   ├── store/store.go             # 持久化：账号池、密钥、设置、统计、日志
│   ├── registry/registry.go       # 模型清单与别名解析（最长匹配 / 别名链）
│   └── server/
│       ├── server.go              # 路由、鉴权、CORS、控制台 API
│       ├── upstream.go            # runUpstream：选号 / 超时 / 日志 / 错误响应统一入口
│       ├── collector.go           # responseCollector：文本与结构化产物汇总去重
│       ├── chat.go                # /v1/chat/completions（流式 + 阻塞）
│       ├── anthropic.go           # /v1/messages 双向转换
│       ├── handlers_skills.go     # /v1/skills、/v1/capabilities
│       ├── handlers_conversations.go # /v1/conversations*
│       ├── handlers_credits.go    # /v1/credits*、/v1/checkin、/v1/tasks*
│       ├── autorewards.go         # 后台自动签到 / 自动领取调度
│       ├── images.go              # /v1/images/generations
│       ├── videos.go              # /v1/videos/generations
│       ├── music.go               # /v1/music/*
│       ├── login.go               # 扫码登录（passport SSO）
│       ├── web.go                 # 控制台单页与 favicon 的内嵌及响应头
│       ├── extras.go              # 空壳：记录杂项端点的拆分去向
│       ├── web/index.html         # 控制台单页
│       └── web/favicon.ico        # 控制台图标（16/32/48，内嵌）
├── assets/logo.png                # 项目 Logo（README 使用，取自客户端图标）
├── cmd/probe/                     # 上游探测工具（逆向排障用，非网关功能）
├── tools/extract_pak.py           # biz.pak 解包脚本（第一步，可重跑）
├── tools/extract_sources.py       # source map → 源码还原脚本（第二步，可重跑）
├── docs/                          # 本文档目录（recon / api / security / roadmap / openapi.yaml）
├── scripts/
│   ├── fmt-check.sh               # gofmt 校验（CI 与 make check 共用）
│   ├── release.sh                 # 打 annotated tag（校验 CHANGELOG 小节 + make check）
│   ├── console-selftest.mjs       # 控制台点击回归（CDP 驱动无头 Chrome）
│   ├── recon_pak_probe.py         # 按关键字定位 pak 内 blob
│   └── recon_ctx.py               # 打印 minified JS 的可读切片
├── Makefile                       # 常用任务：build / test / race / cover / dist / release / docker
├── .github/workflows/
│   ├── ci.yml                     # CI：fmt / vet / race / build / docker
│   └── release.yml                # 推 v* tag 自动建 Release 并上传产物
├── .markdownlint.yaml             # 文档 lint 白名单（居中徽章版式 / CHANGELOG 重复标题）
├── .yamllint                      # YAML lint 配置（OpenAPI 紧凑流程映射）
├── doubao2api-data.example.json   # 脱敏状态文件样例
└── go.mod                         # 无 require，纯标准库
```
