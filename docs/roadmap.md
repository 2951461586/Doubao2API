# 进度与后续

> 已发布版本见 [CHANGELOG](../CHANGELOG.md)。本节只记「做到了什么」与「还没做什么」，
> 实现细节见 [recon.md](recon.md)，接口见 [api.md](api.md)。

## 版本

| 版本 | 日期 | 主题 |
| --- | --- | --- |
| v0.1.4 | 2026-11-14 | 安全基线、错误分型、流式预检、模型清单对齐、备份与可观测 |
| v0.1.3 | 2026-11-14 | SSE 解析/编排加固、请求级 agent_mode、结构化流式错误、/health 可观测 |
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

### v0.1.4 完成

- [x] 管理密码 PBKDF2 哈希、API 密钥只存指纹（完整密钥仅创建时返回一次）
- [x] 管理面未设密码时一律拒绝；管理密码不再能调用 `/v1/*`
- [x] 可信代理解析（`trusted_proxies` / `admin_loopback_only`），堵住反代回环误放行
- [x] 管理鉴权爆破防护（5 次/5 分钟）与按密钥限流
- [x] 上游错误分型（business / account / timeout / canceled / transport）+ 日志 `kind`
- [x] 客户端断开不计入失败率；流式请求先探测再提交 200
- [x] 总预算 `overall_timeout_sec`、换号不重复、失败冷却、并发闸门、退避抖动
- [x] `sid_guard` 到期推算 + `/health` 告警 + `/readyz`
- [x] 访问日志（逐请求一行、错误脱敏）
- [x] `.bak` + 按日 `backups/` + 导出/导入 + 30 秒合并落盘
- [x] `tools` 不再静默丢弃（默认 422）
- [x] 覆盖率门槛与 `FuzzReadSSE`
- [x] 模型清单与桌面端缓存逐项对齐（名称 / key / 上下文 / 推理档位）
- [x] `probe -client-models` 差异比对与 `TestRegistryCoversClientModels`
- [x] 并发选号分散到不同账号（选中即占用）

### 待实现

- [ ] **工具调用**：当前客户端是「本地沙箱执行 + 结果回传」
      （`/alice/office/tool_local/upload_tool_call_result(_v2)`），网关没有沙箱环境；
      要支持 OpenAI `tools` 语义需先解决沙箱执行面与结果格式
- [ ] 异步长任务：`/chat/async/chunk_stream`、`/samantha/chat/async/stream`
- [ ] 上游二进制流 `/alice/message/stream_reply`（protobuf），当前仍走 JSON SSE
- [x] SSE 里非 JSON 的 `data:` 事件：已加计数并暴露到 `/health`
      （`non_json_events`），避免协议漂移时表现为「回答空白」而无告警
- [x] `server` 包覆盖率：已用假上游补上 `/v1/chat/*` 编排逻辑的测试
      （`agent_mode` 归一、流式帧序、换号重试、附件失败、结构化错误帧）
- [ ] 密钥哈希存储 + 管理接口失败限流（需改控制台交互）
