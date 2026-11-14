# 安全模型

> 本项目持有账号 Cookie 与 API 凭据，默认配置刻意偏保守。
> 修改任何默认值前，请先读完本文。

## 1. 凭据存储

状态文件（`doubao2api-data.json`）**不再保存任何明文凭据**：

| 凭据 | 存储形式 | 说明 |
| --- | --- | --- |
| 管理密码 | `admin_password_hash`（PBKDF2-HMAC-SHA256，210k 轮，随机盐） | 校验用恒定时间比较；旧版的 `admin_password` 明文字段在 `Open` 时自动迁移并清空 |
| API 密钥 | `key_hash`（SHA-256）+ `display`（`sk-abc123…wxyz`） | **完整密钥只在创建时返回一次**；列表与状态接口只回展示片段 |
| 账号 Cookie | 原样保存（需要回放给上游） | 状态文件仍等同账号密码，见下节 |

> PBKDF2 用 `crypto/hmac` + `crypto/sha256` 自行实现（约 40 行），
> 以维持「零第三方依赖」的约束（CI 会检查 `go.mod` 没有 `require`）。

### 状态文件保护

```text
doubao2api-data.json        主文件（0600）
doubao2api-data.json.bak    上一版（每次保存覆盖）
backups/data-*.json         按日快照，保留 7 份（目录 0700）
```

- 所有写入都是 `tmp → fsync → rename`，进程崩溃不会留下半截 JSON。
- 请求级状态（账号失败计数、统计、密钥用量）每 30 秒合并落盘，
  不再只在正常退出时保存。
- 控制台 API 提供 `snapshot` / `export` / `import`（导入必须 `confirm=1`，
  且默认保留当前管理密码，避免被旧导出锁死）。
- 备份与导出同样含 Cookie，**按密码对待**：不要提交、不要放公开网盘。

## 2. 谁在调用：来源判定

「仅本机」不能只看 `r.RemoteAddr`：只要网关前面有反向代理
（Tailscale Funnel / Serve、nginx、Caddy、宿主端口转发），
`RemoteAddr` 都会变成 `127.0.0.1`，回环判断就会把公网请求当成本机。

现在的判定规则（`internal/server/trust.go`）：

1. 没有代理头 → 用 `RemoteAddr`；
2. 有 `X-Forwarded-For` / `X-Real-IP` / `Forwarded` 但来源不在
   `trusted_proxies` 内 → **一律按远程处理**；
3. 来源是可信代理 → 采信最左侧的 `X-Forwarded-For`。

由此：

- **无密钥模式**（一把密钥都没签发）：`/v1/*` 只放行「本机直连」
  （无代理头且来源是回环），或显式开启 `allow_anonymous`。
- **管理面**：未设置管理密码时**一律拒绝**（503），不再有「本机可用」的退路。
  设置密码后，`admin_loopback_only=true`（裸机默认）只接受本机直连；
  容器 / 反代部署需显式关闭，并配置 `trusted_proxies`。

## 3. 认证与限流

- `/v1/*`：`Authorization: Bearer sk-...` 或 `x-api-key`。
  **管理密码不能用于调用 `/v1/*`**：管理凭据只进管理面，
  否则一次密码泄露就等于 API 泄露。
- `/admin/api/*`：`X-Admin-Password` 或 `Authorization: Bearer <密码>`；
  同一来源 5 分钟内失败 5 次 → `429` + `Retry-After`。
- 可选 `rate_limit_per_key`（每分钟请求数，默认 0 = 不限）。
- 写操作额外做同源校验（`Origin` 必须与 `Host` 同源或命中 CORS 允许列表），
  阻断 CSRF；非浏览器客户端通常不带 `Origin`，不受影响。

## 4. CORS

- 默认只放行本机来源（`localhost` / `127.0.0.1` / `::1`，任意端口）。
- 需局域网 / 域名访问时在设置里显式填逗号分隔的来源；`*` 不生效。
- **从不发送** `Access-Control-Allow-Credentials`。
- 未配置来源时 `/health` 会给出 `cors_same_origin_only` 提示（浏览器直连会失败，
  curl / SDK 不受影响）。

## 5. 日志与脱敏

- 进程日志每个请求一行：`id / ip / method / path / status / kind / ms / key / acct / in / out / err`。
- 密钥只输出脱敏片段；错误文本过 `redact`：命中 `sessionid` / `sid_guard` /
  `ttwid` / `passport_csrf_token` / `Authorization` / `Cookie` 等键时，
  **值整段替换为 `***`**（`Authorization` 这类头的值可能含空格，
  会一直吃到行尾，避免残留 `Bearer sk-...`）。
- 状态文件里的请求日志（默认上限 500 条）只记元信息，不落对话内容。
- 控制台轮询端点默认不写访问日志（`verbose_access_log=true` 可打开）。

## 6. 运行环境

- Docker 模板默认：只发布到 `127.0.0.1`、`read_only`、`tmpfs /tmp`、
  `cap_drop: ALL`、`no-new-privileges`、日志轮转。
- 不要把管理面直接放到公网：`admin_loopback_only` 开启时仅本机；
  必须远程管理时，请在前面加一层带鉴权的反向代理并配置 `trusted_proxies`。
- 外部客户端一律使用 `https://`（Tailscale Funnel 只在 443 提供服务，
  访问 `http://` 会得到空响应，不是重定向）。

---

## 注意事项

- **本项目仅供本机 / 内网自用与协议研究。** 上游服务的使用受豆包服务条款约束，
  请勿公开暴露到公网或用于商业转售。
- 凭据即账号。`doubao2api-data.json` 及其备份里的 Cookie 等同于账号密码，
  **不要提交到仓库或分享**。
- 逆向产物 `.recon/`（`biz.pak` 解包结果 `extract/` 约 525 MB、source map
  还原的第三方源码 `src/` 约 16 MB）体积大且含第三方版权内容，已加入 `.gitignore`，
  仅作本地协议比对；两者都可由 `tools/` 下两个脚本重新生成，不入库也不影响构建。
- 上游可能随时调整协议或启用风控。若出现 `gateway-error`，
  请重新登录桌面端后重新导入账号。
