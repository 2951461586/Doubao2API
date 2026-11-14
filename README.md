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
| `/v1/models` | GET | 模型清单（含技能模型与自定义别名） |
| `/v1/images/generations` | POST | 文生图（OpenAI Images 兼容） |
| `/v1/videos/generations` | POST | 文生视频 |
| `/v1/music/lyrics`、`/v1/music/audio` | POST | 歌词生成 / 音频地址 |
| `/v1/skills`、`/v1/capabilities` | GET | 技能清单与能力自发现 |
| `/v1/conversations*` | GET 等 | 会话列表 / 重命名 / 归档 / 置顶 |
| `/v1/credits`、`/v1/credits/history` | GET | 积分余额 / 流水 |
| `/v1/checkin`、`/v1/tasks*` | POST / GET | 签到与积分任务 |
| `/health`、`/ping` | GET | 探活 |
| `/admin/api/*` | 多种 | 控制台 API（管理鉴权 + 同源校验） |
| `/` | GET | 控制台单页 |

完整字段与示例见 [docs/api.md](docs/api.md)。

## 工程质量

```bash
make check        # gofmt + go vet + go test -race（CI 同款）
make cover        # 覆盖率报告
```

当前覆盖率（`go test -cover ./...`）：`account` / `ids` 为叶子包，
`registry` ≈ 96%、`store` ≈ 93%、`server` ≈ 30%、`doubao` ≈ 22%；
`doubao` 的上游协议路径靠 `internal/doubao/testdata/*.sse` 夹具做回归。


## 快速开始

### 1. 编译与运行

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

### 2. 只导入一次

```bash
./doubao2api -import          # 自动探测并导入后退出
./doubao2api -import -doubao-dir "D:\path\to\User Data"
```

### 3. 调用

```bash
curl http://127.0.0.1:10086/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"doubao-think","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

### 4. Docker 一键部署

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

控制台内还可调：模型别名、请求超时、重试次数、日志保留条数、CORS 来源、
自动签到（`auto_checkin`）、自动领取已完成任务奖励（`auto_claim_tasks`）、
自动做任务（`auto_do_tasks`）、待确认签到的后台重试（`retry_pending_checkin`）、
无密钥模式是否放行非本机（`allow_anonymous`）。
## 许可证

本项目以 [Apache License 2.0](LICENSE) 授权发布。许可证覆盖的是**本项目自身的代码**；
逆向过程中接触到的第三方内容（豆包客户端二进制、`biz.pak` 产物、source map
还原出的源码）不在本仓库内，其权利仍归原权利人。

> 许可与使用限制是两件事：Apache-2.0 授予你使用、修改、分发本代码的权利，
> 但并不豁免你对上游服务条款的义务。请遵守 [docs/security.md](docs/security.md) 的注意事项。
