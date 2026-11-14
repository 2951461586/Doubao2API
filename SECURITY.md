# Security Policy

## 这个项目持有什么敏感数据

Doubao2API 复用本机豆包桌面端的**已登录会话**，因此状态文件
（默认 `doubao2api-data.json`）里同时包含：

| 数据 | 风险等级 | 说明 |
| --- | --- | --- |
| `accounts[].cookies`（`sessionid` / `sid_guard` / `ttwid` …） | 等同账号密码 | 拿到即可冒用该账号调用上游 |
| `keys[].key`（`sk-…`） | 等同网关调用权 | 明文存储，仅创建时回显一次 |
| `settings.admin_password` | 管理权限 | 可直接读取全部状态与操作账号 |

**这个文件不要提交、不要同步到云盘、不要贴进 issue。**
仓库已在 `.gitignore` 中排除它，并提供了脱敏样例 `doubao2api-data.example.json`。

## 报告漏洞

请**不要**在公开 issue 里贴出 Cookie、API 密钥、完整状态文件或上游响应原文。

- 优先使用 GitHub 的 [私密漏洞报告](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/about-coordinated-disclosure-of-security-vulnerabilities)（Security → Advisories → New draft）。
- 无法使用私密通道时，开一个只描述**影响面**与**复现前提**的 issue，并写明「细节需私下同步」。

我们会在 7 天内确认收到，并在修复发布后于 CHANGELOG 标注。

## 默认安全基线（请勿轻易放宽）

| 项 | 默认 | 说明 |
| --- | --- | --- |
| 监听地址 | `127.0.0.1` | 默认只绑本机 |
| 无密钥模式 | 仅放行环回来源 | 未签发密钥时 `allow_anonymous` 默认关闭 |
| 管理接口 | 未设密码时仅本机 | 设置 `admin_password` 后才放开给非本机 |
| 写操作 | 同源校验 | 非 GET/HEAD/OPTIONS 均校验 `Origin` |
| CORS | 仅本机来源 | 不提供通配符，从不发送 `Access-Control-Allow-Credentials` |
| 密钥校验 | 恒定时间比较 | 先 SHA-256 再 `subtle.ConstantTimeCompare` |
| 状态文件权限 | `0600` | 原子写（临时文件 + rename） |

放宽任何一项（例如 `-host 0.0.0.0` + `allow_anonymous=true`）都等于把账号
交给整个网络。详见 [docs/security.md](docs/security.md)。

## 上游与合规

本项目仅用于**本机 / 内网自用与协议研究**。上游服务的使用受豆包服务条款约束，
请勿公开暴露到公网或用于商业转售。逆向产物（`.recon/`）不入库，其版权归原权利人。
