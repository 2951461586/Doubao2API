# 进度与后续

> 已完成项按阶段归档；未完成项即后续计划。

## 当前进度与后续

### 已完成（第二阶段：协议对齐 + 能力扩充）

- [x] `biz.pak` v5 格式兼容（哨兵结束 / field2 非条目数），拆包脚本可重跑
- [x] 公参 / 请求头对齐 147 客户端：`aid=1044603`、`doubao_device_platform`、
      `pc_version=2.36.15`、`runtime_version=3.45.5`、`channel=win`、
      `fp=verify_<device_id>`、`iid`、`Agw-Js-Conv: str`、`tz_name`
- [x] CSRF token 自动刷新（`/passport/safe/csrf_token/`，去掉 `real_aid`）+ 30 分钟缓存 + 鉴权失败重试
- [x] 旧账号指纹自动迁移（随机 `fp` → `verify_<device_id>`，补齐 `web_tab_id`）
- [x] 新块类型解析：搜索来源 / 代码 / 生图 / 产物 / 链接引用 / 音乐成品
- [x] 结构化输出：`sources[]` / `artifacts[]` / `music[]` / `images[]`
- [x] 技能清单与技能模型（13 个技能模型，自动套用 `default_prompt`）
- [x] `reasoning_effort` → `need_deep_think` 映射
- [x] `agent_mode` / `model_item_key` / `mode_id` / `selection_mode` / `selected_skills` / `ext` 透传
- [x] `/v1/skills`、`/v1/conversations`（列表 / 重命名 / 归档 / 置顶 / 清空）、`/v1/capabilities`
- [x] `/v1/music/lyrics`（lyrics_web / lyrics，实测可用）
- [x] 积分 / 签到 / 任务奖励：`/v1/credits`、`/v1/checkin`、`/v1/tasks`、
      `/v1/tasks/claim`、`/v1/tasks/claim-completed`
- [x] 每日自动签到 / 自动领取已完成奖励（`auto_checkin` / `auto_claim_tasks`，默认关闭）
- [x] 自动尝试做任务（`auto_do_tasks` / `auto_task_limit`）+ 会话清空归档 + 临时文件清理
- [x] 控制台全面改版：侧边栏、深浅色、概览 / 账号 / 密钥 / 模型与技能 /
      积分与任务 / 会话 / 日志 / 设置，覆盖全部后端模块

### 第一阶段（精简网关）

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

- [ ] 文生音乐成品：`skill=9` 的聊天请求仍受账号配额 / 风控限制；
      歌词（`/v1/music/lyrics`）与成品解析（`music[]` / `/v1/music/audio`）已闭环，
      有配额的账号可直接出成品
- [ ] 异步长任务：`/chat/async/chunk_stream` / `/samantha/chat/async/stream`
      （新版客户端的 async task 协议，用于智能体 / 深度研究长任务）
- [ ] 上游二进制流：`/alice/message/stream_reply`（protobuf），当前仍走 JSON SSE

---
