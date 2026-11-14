<div align="center">
  <img src="assets/logo.png" width="112" height="112" alt="Doubao2API">
  <h1>Doubao2API</h1>
  <p><strong>把本机豆包（DoubaoWork）桌面端的上游模型服务，复刻成 OpenAI 兼容的 Go 网关</strong></p>

  <p>
    <a href="https://github.com/2951461586/Doubao2API/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/2951461586/Doubao2API/actions/workflows/ci.yml/badge.svg?branch=master"></a>
    <a href="https://github.com/2951461586/Doubao2API/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/2951461586/Doubao2API?sort=semver"></a>
    <img alt="Go" src="https://img.shields.io/github/go-mod/go-version/2951461586/Doubao2API">
    <img alt="Dependencies" src="https://img.shields.io/badge/dependencies-0-brightgreen">
    <img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue">
  </p>

  <p>
    <img alt="API" src="https://img.shields.io/badge/API-OpenAI%20%2F%20Anthropic-412991">
    <img alt="Platforms" src="https://img.shields.io/badge/platform-Windows%20%7C%20Linux%20%7C%20macOS-lightgrey">
  </p>
</div>

<p align="center">
  复用桌面端已登录的会话凭据，无需另申请密钥；纯 Go 标准库实现，单文件约 10 MB，零第三方依赖。<br>
  当前版本 <b>v0.1.4</b> ·
  <a href="https://github.com/2951461586/Doubao2API/releases/latest">下载预编译产物</a> ·
  <a href="CHANGELOG.md">更新日志</a> ·
  <a href="docs/api.md">接口文档</a>
</p>


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

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [docs/api.md](docs/api.md) | 接口契约：模型 / 别名 / 全部端点与字段 |
| [docs/openapi.yaml](docs/openapi.yaml) | 机器可读的 OpenAPI 3.1 描述 |
| [docs/security.md](docs/security.md) | 安全模型：鉴权、CORS、脱敏、注意事项 |
| [docs/recon.md](docs/recon.md) | 逆向结论与实现要点（协议、快照、项目结构） |
| [docs/roadmap.md](docs/roadmap.md) | 进度与后续计划 |
| [SECURITY.md](SECURITY.md) | 漏洞报告流程 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 开发、测试、还原脚本与提交约定 |
| [CHANGELOG.md](CHANGELOG.md) | 版本变更记录 |

## 端点速查

| 端点 | 方法 | 用途 |
| --- | --- | --- |
| `/v1/chat/completions` | POST | OpenAI Chat Completions（流式 / 非流式） |
| `/v1/messages` | POST | Anthropic Messages（流式 / 非流式） |
| `/v1/models` | GET | 模型清单（与桌面端「工作任务」列表逐项对齐，含技能模型与自定义别名） |
| `/v1/images/generations` | POST | 文生图（OpenAI Images 兼容） |
| `/v1/videos/generations` | POST | 文生视频 |
| `/v1/music/lyrics`、`/v1/music/audio` | POST | 歌词生成 / 音频地址 |
| `/v1/skills`、`/v1/capabilities` | GET | 技能清单与能力自发现 |
| `/v1/conversations*` | GET 等 | 会话列表 / 重命名 / 归档 / 置顶 |
| `/v1/credits`、`/v1/credits/history` | GET | 积分余额 / 流水 |
| `/v1/checkin`、`/v1/tasks*` | POST / GET | 签到与积分任务 |
| `/health`、`/ping` | GET | 探活（`/health` 附可行动告警；`/ping` 极简） |
| `/readyz` | GET | 就绪检查：无可用账号时 503 |
| `/admin/api/*` | 多种 | 控制台 API（管理鉴权 + 同源校验） |
| `/` | GET | 控制台单页 |

完整字段与示例见 [docs/api.md](docs/api.md)。

## 工程质量

```bash
make check        # gofmt + go vet + go test -race（CI 同款）
make cover        # 覆盖率报告

# 模型清单对齐：读取本机客户端缓存并和 registry 比对
go run ./cmd/probe -models
go run ./cmd/probe -client-models
```

当前覆盖率（`go test -cover ./...`）：`registry` ≈ 96%、`account` ≈ 92%、
`store` ≈ 92%、`server` ≈ 64%、`doubao` ≈ 40%；
`doubao` 的上游协议路径靠 `internal/doubao/testdata/*.sse` 夹具做回归，
并用 `FuzzReadSSE` 保证分帧器对任意字节序列不 panic。

CI 会跑 `scripts/coverage-gate.sh`：`server` / `doubao` / `store` / `account`
的覆盖率低于门槛会直接失败，避免重构悄悄吃掉测试。


## 快速开始

### 1. 下载预编译产物（推荐）

从 [Releases](https://github.com/2951461586/Doubao2API/releases/latest) 下载对应平台：

| 平台 | 文件 |
| --- | --- |
| Windows x64 | `doubao2api-vX.Y.Z-windows-amd64.exe` |
| Linux x64 | `doubao2api-vX.Y.Z-linux-amd64` |
| macOS（Apple Silicon） | `doubao2api-vX.Y.Z-darwin-arm64` |

每个 Release 同时提供 `SHA256SUMS`，下载后可校验：

```bash
sha256sum -c SHA256SUMS          # Linux
shasum -a 256 -c SHA256SUMS      # macOS
```

> 没有对应平台？用下面的源码编译，或直接跑 Docker（第 5 节）。

### 2. 从源码编译

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

### 3. 只导入一次

```bash
./doubao2api -import          # 自动探测并导入后退出
./doubao2api -import -doubao-dir "D:\path\to\User Data"
```

### 4. 调用

```bash
curl http://127.0.0.1:10086/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"doubao-think","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

### 5. Docker 一键部署

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
| 健康检查 | `GET /ping`（免鉴权）；`GET /readyz` 反映是否有可用账号 |
| 自动导入 | 容器内**不可用**（DPAPI 是 Windows 专有），故用 `-no-import` 启动 |
| 管理密码 | **必须**通过 `DOUBAO_ADMIN_PASSWORD` 设置；未设置时管理接口一律 503 |
| 管理面来源 | 容器内来源是网桥地址，compose 已设 `DOUBAO_ADMIN_LOOPBACK_ONLY=false`；裸机保持默认 `true` |
| 容器加固 | compose 模板已启用 `read_only`、`tmpfs /tmp`、`cap_drop: ALL`、`no-new-privileges` 与日志轮转 |

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
    >
    > 可选字段：`device_id` / `web_id` / `iid` / `app_id` / `channel`。填写真实的
    > `device_id` 后网关会自动把 `fp` 设为 `verify_<device_id>`（与桌面端一致）；
    > 默认 `app_id=1044603`（DoubaoWork），主端 Web Cookie 可显式传 `app_id=582478`。

3. 验证连通性：

    ```bash
    curl -X POST http://127.0.0.1:10086/admin/api/checkin \
      -H "Content-Type: application/json" \
      -H "X-Admin-Password: $DOUBAO_ADMIN_PASSWORD" -d '{}'
    ```

> 未提供 `device_id` / `web_id` 时网关会生成随机占位值（上游可接受）。
> 若上游启用风控并返回 `gateway-error`，请在桌面端重新登录后重新导出 Cookie。

---

## 配置

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

另有环境变量（优先级高于状态文件）：

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `DOUBAO_ADMIN_LOOPBACK_ONLY` | `true` | 管理接口是否只接受本机直连；容器部署需设为 `false` |
| `DOUBAO_TRUSTED_PROXIES` | 空 | 可信反向代理网段（CIDR，逗号分隔）。为空时任何带 `X-Forwarded-For` 的请求都不算本机 |

控制台内还可调：模型别名、单次请求超时（`request_timeout_sec`）、**总预算**
（`overall_timeout_sec`，含换号重试）、重试次数、日志保留条数、CORS 来源、
可信代理网段（`trusted_proxies`）、管理面回环限制（`admin_loopback_only`）、
凭据到期预警天数（`session_warn_days`）、单账号并发上限
（`max_concurrent_per_account`）、按密钥限流（`rate_limit_per_key`）、
工具字段策略（`tools_policy`：`reject` / `ignore`）、
自动签到（`auto_checkin`）、自动领取已完成任务奖励（`auto_claim_tasks`）、
自动做任务（`auto_do_tasks`）、待确认签到的后台重试（`retry_pending_checkin`）、
无密钥模式是否放行非本机（`allow_anonymous`）、详细访问日志（`verbose_access_log`）。

### 状态文件与备份

- 管理密码只保存 **PBKDF2-HMAC-SHA256** 哈希；API 密钥只保存 **SHA-256 指纹**
  与展示片段（`sk-abc123…wxyz`），完整密钥只在创建时返回一次。
- 每次保存都会保留一份 `<data>.json.bak`（上一版），并按天生成
  `backups/data-<时间戳>.json`（保留 7 份）。
- 控制台 API：`POST /admin/api/state/snapshot`、`GET /admin/api/state/export`、
  `POST /admin/api/state/import?confirm=1&merge=1`（导入默认保留当前管理密码）。
- 请求级状态（账号失败计数、统计、密钥用量）每 30 秒合并落盘，不再等到退出。

### 排障

- 容器日志里每个请求一行：`request id=… path=… status=… kind=… ms=… acct=…`。
  `kind` 区分 `ok` / `business`（上游业务错误）/ `account`（会话/额度）/
  `timeout` / `canceled`（客户端断开）/ `transport`（网络）。
- `/health` 的 `warnings[]` 会列出「Cookie 快到期」「未设管理密码」等可行动事项；
  `degraded` 为真时看一眼。
## 许可证

本项目以 [Apache License 2.0](LICENSE) 授权发布。许可证覆盖的是**本项目自身的代码**；
逆向过程中接触到的第三方内容（豆包客户端二进制、`biz.pak` 产物、source map
还原出的源码）不在本仓库内，其权利仍归原权利人。

> 许可与使用限制是两件事：Apache-2.0 授予你使用、修改、分发本代码的权利，
> 但并不豁免你对上游服务条款的义务。请遵守 [docs/security.md](docs/security.md) 的注意事项。

项目 Logo（`assets/logo.png` 与控制台 favicon）取自豆包客户端图标，仅用于标识本项目，
**不在 Apache-2.0 授权范围内**，其权利归原权利人；如需再分发请自行评估。
