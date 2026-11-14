# Changelog

本项目的变更记录。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 `主.次.修订`（`internal/` 重构不改变对外契约时为修订号）。

## [Unreleased]

### Changed（架构与工程化）

- **持久化层与协议层解耦**：账号模型移入新包 `internal/account`，
  `internal/store` 不再依赖 `internal/doubao`；状态文件格式不再随上游逆向结论漂移。
- **运行时状态不再落盘**：CSRF token 与 tab id 迁入进程内 `runtime`（副本间共享），
  状态文件不再写入 `csrf_token` / `csrf_token_at`。
- **账号快照语义**：`Accounts()` / `Account()` / `NextAccount()` 一律返回深拷贝，
  新增 `ToggleAccount` / `SetAccountEnabled` / `ClearAccountErrors` 作为写入口。
- `internal/server/extras.go`（845 行）按职责拆分为
  `handlers_skills.go` / `handlers_conversations.go` / `handlers_credits.go` /
  `autorewards.go` / `upstream.go` / `collector.go`。
- 新增 `runUpstream`：选号、超时、日志、错误响应统一收口，端点里不再重复样板。
- 新增 `responseCollector`：阻塞式与流式对话共用同一套产物累积 / 去重逻辑。
- 补丁流解析改为**块类型注册表**（`stream_blocks.go`），新增块类型只需注册一个函数。

### Fixed

- **数据竞争**：store 曾把内部账号指针交给调用方，handler 在锁外改字段会与
  轮询、`Save()` 的 JSON 序列化并发读写同一块内存。现已用「深拷贝 + 写方法」
  消除，并加入 `-race` 并发回归用例。
- **CSRF 缓存失效**：`Account.Clone()` 在原账号尚未初始化运行时状态时会给副本
  新建一份，导致每个请求都重新刷新 token（多一次上游往返）。
- **模型别名最长匹配**：`doubao-think-pro` 这类名字曾被短 ID `doubao` 抢先命中，
  解析为快速模式；现在按最长匹配 + 字典序取稳定结果。
- `/v1/models` 的自定义别名顺序不再随机（map 遍历顺序导致抖动）。
- 自定义别名支持**别名链**（含环路保护与深度上限），并统一大小写归一化。

### Fixed（积分 / 签到，实测发现）

- `/v1/checkin`：上游 `/alice/ug/campaign/landing/checkin` 已废弃（恒返回
  `710010702 system error`），改走活动任务路径（`daily_signin`：
  `event/report` 创建实例 + 奖励领取），旧接口保留为回退。
- 领取结果分类：`biz_error.code=3`（「领取结果确认中」）此前被当成失败，
  现在映射为 `pending` 并做有限重试；`ClaimResult.status` 增加 `pending`，
  `claim-completed` 返回 `pending_count` / `failed_count`，
  `/v1/checkin` 返回 `pending` 字段。
- `CampaignTask` 增加 `daily_checkin`（`checked_in_today` / `continuous_days` /
  `last_checkin_date` / `reward`），`/v1/tasks` 可直接判断今天是否已签到。
- `/v1/checkin` 只在 `checked_in && !pending` 时写入账号签到日期，
  避免「已受理待确认」被误记成今天已签到。

### Added

- `internal/ids`：随机标识叶子包（UUID / 十六进制 / 数字串）。
- `internal/registry` 测试（覆盖率 0% → 96%）：别名、别名链、最长匹配、清单稳定性。
- `internal/account` 测试：深拷贝隔离、运行时共享、指纹归一化、CSRF TTL。
- `internal/doubao/testdata/*.sse` 夹具 + 表驱动测试：文本、思维链、搜索来源、
  代码块、生成产物、音乐/产物、业务错误、网关错误、内容补丁、工具提示。
- 文档拆分：`docs/api.md`、`docs/recon.md`、`docs/security.md`、`docs/roadmap.md`、
  `docs/openapi.yaml`，以及 `SECURITY.md`、`CONTRIBUTING.md`、`CHANGELOG.md`。
- `Makefile`（`build` / `check` / `race` / `cover` / `docker` / `probe`）与
  GitHub Actions CI（gofmt / vet / race / build / docker）。

## [0.9.0] - 2025-11-13

### Added

- 扫码登录（passport SSO）与控制台全面改版。
- 积分 / 签到 / 任务奖励端点，后台自动签到与自动领取（默认关闭）。
- 新块类型解析：搜索来源、代码、生图、产物、链接引用、音乐成品。
- 文生图 / 文生视频 / 音乐歌词与音频端点。
- Anthropic `/v1/messages` 兼容层。

## [0.1.0] - 2025-10-06

### Added

- 首个可用版本：桌面端登录态导入（DPAPI + AES-GCM + 域名前缀剥离）、
  上游协议复刻（公参 / 请求体 / SSE 补丁流）、OpenAI 兼容网关、
  控制台单页、Docker 一键部署、Apache-2.0 授权。
