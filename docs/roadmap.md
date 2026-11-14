# 进度与后续

> 已发布版本见 [CHANGELOG](../CHANGELOG.md)。本节只记「做到了什么」与「还没做什么」，
> 实现细节见 [recon.md](recon.md)，接口见 [api.md](api.md)。

## 版本

| 版本 | 日期 | 主题 |
| --- | --- | --- |
| v0.1.2 | 2026-11-14 | 控制台侧栏修复、协议重新取证、发布自动化、品牌资源 |
| v0.1.0 | 2026-11-14 | 首个正式版本（网关 + 控制台 + 文档） |

## 已完成

### 协议与网关

- 桌面端登录态导入（DPAPI + AES-GCM + 域名前缀剥离，支持多 profile）
- 公参 / 请求头 / SSE 补丁流复刻；CSRF 自动刷新 + 30 分钟缓存 + 鉴权失效重试
- 三档思考（快速 / 思考 / 专家）→ `reasoning_content`；技能自动套用 `default_prompt`
  （12 个技能模型）
- 多轮：历史压平 + `conversation_id` 原生续接
- 账号池轮询 + 有界重试（客户端断开立即停止）
- 块类型注册表（30 种 `block_type`）：文本 / 思维链 / 搜索来源 / 代码 / 生图 / 产物 / 音乐
- 结构化输出 `sources[]` / `artifacts[]` / `music[]` / `images[]`，流式与阻塞一致

### 端点

- OpenAI：`/v1/chat/completions`、`/v1/models`、`/v1/images/generations`
- Anthropic：`/v1/messages`（含 thinking 块与图片）
- 豆包原生：`/v1/videos/generations`、`/v1/music/lyrics`、`/v1/music/audio`、
  `/v1/skills`、`/v1/capabilities`、`/v1/conversations*`
- 积分：`/v1/credits*`、`/v1/checkin`、`/v1/tasks*`（含 `pending` 语义与后台复查队列）

### 控制台与运维

- 单页控制台（内嵌、零外部资源、深浅色、favicon、侧栏角标）
- 扫码登录、手工 Cookie、多 profile 导入、逐账号探活
- 鉴权：密钥 / 管理密码 / 无密钥仅本机 / 写操作同源校验 / CORS 收紧 / 恒定时间比较
- Docker 部署、`Makefile`、CI（fmt / vet / race / build / docker）、
  推 tag 自动发布（构建三平台产物 + `SHA256SUMS` + 建 Release）

### 工程质量

- 覆盖率：`registry` 96%、`account` 95%、`store` 93%、`server` 33%、`doubao` 22%
- 协议夹具回归：`internal/doubao/testdata/*.sse`（10 份事件序列）
- 控制台点击回归：`scripts/console-selftest.mjs`（CDP 驱动无头 Chrome）
- 文档：`api` / `recon` / `security` / `roadmap` / `openapi.yaml` +
  `SECURITY.md` / `CONTRIBUTING.md` / `CHANGELOG.md`

## 后续

### 阻塞在上游

- [ ] **签到动作**：`/alice/ug/campaign/landing/checkin` 已废弃，任务停在
      `in_progress`，领取恒返回 `biz_code=3`（确认中）。网关如实返回 `pending`，
      `retry_pending_checkin` 开启后每小时复查，上游恢复即自动补写
- [ ] **积分活动**：部分环境（如部署机出口 IP）`/v1/tasks` 返回
      `campaign not found`；同一账号在本机直连正常，判断为环境级差异
- [ ] **文生音乐成品**：`skill=9` 受会员配额门禁（`710022004 rate limited`）；
      歌词与成品解析已闭环，有配额的账号可直接出成品

### 待实现

- [ ] **工具调用**：当前客户端是「本地沙箱执行 + 结果回传」
      （`/alice/office/tool_local/upload_tool_call_result(_v2)`），网关没有沙箱环境；
      要支持 OpenAI `tools` 语义需先解决沙箱执行面与结果格式
- [ ] 异步长任务：`/chat/async/chunk_stream`、`/samantha/chat/async/stream`
- [ ] 上游二进制流 `/alice/message/stream_reply`（protobuf），当前仍走 JSON SSE
- [ ] SSE 里非 JSON 的 `data:` 事件目前静默跳过：加计数并暴露到 `/health`，
      避免协议漂移时表现为「回答空白」而无告警
- [ ] 密钥哈希存储 + 管理接口失败限流（需改控制台交互）
- [ ] `server` 包覆盖率：用假上游补 `/v1/chat/*` 编排逻辑的测试
