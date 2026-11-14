# Changelog

本项目的变更记录。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 `主.次.修订`：`internal/` 重构（不改变对外契约）走修订号，
新增对外能力走次版本号，破坏性变更走主版本号。

## [Unreleased]

### Added

- `.github/workflows/release.yml`：推送 `v*` tag 即自动发布 —— 校验 CHANGELOG 小节、
  跑 fmt / vet / `-race`、交叉构建三平台产物与 `SHA256SUMS`，然后建 Release
  （已存在则覆盖附件，兼容重跑）。
- README 新增「下载预编译产物」入口（含各平台文件名与校验方式）。

### Fixed

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
