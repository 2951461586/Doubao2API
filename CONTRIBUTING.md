# 贡献指南

## 环境

- Go 1.24+（`go.mod` 无 `require`，纯标准库，不需要联网拉依赖）
- Windows 才能跑桌面端登录态导入（`internal/doubao/cookie_windows.go`）；
  其他平台由 `cookie_other.go` 提供空实现，编译与测试照常
- 逆向脚本需要 Python 3.9+

## 常用命令

```bash
make build     # 编译（-ldflags 注入版本）
make check     # gofmt + go vet + go test -race（CI 同款）
make cover     # 覆盖率
make docker    # 构建镜像
make probe ARGS='-path /samantha/skill/list -body {}'   # 上游探测
```

没有 make 时等价于 `go build ./... && go vet ./... && go test -race ./...`。

## 目录约定

| 目录 | 职责 | 改动注意 |
| --- | --- | --- |
| `internal/account` | 账号模型：持久化字段 + 运行时不落盘状态 | 不要往持久化结构里加运行期字段 |
| `internal/ids` | 随机标识 | 叶子包，不依赖任何内部包 |
| `internal/store` | 持久化与账号池调度 | 对外只给深拷贝；写操作走 store 方法 |
| `internal/doubao` | 上游协议 | 块类型处理走 `stream_blocks.go` 的注册表 |
| `internal/registry` | 模型清单与别名 | 改内置模型请同步 `docs/api.md` |
| `internal/server` | HTTP 层 | 新端点用 `runUpstream`，别再手写选号/日志 |

## 提交前自检

1. `make check` 全绿（含 `-race`）。
2. 新增/修改上游协议行为时，补一条 `internal/doubao/testdata/*.sse` 夹具。
3. 改了对外字段或端点，同步 `docs/api.md` 与 `docs/openapi.yaml`。
4. 不要把 `.recon/`、状态文件、真实 Cookie、上游原始响应贴进仓库。

## 逆向脚本

```bash
# 1) 拆包（产物约 525 MB，勿入库）
python tools/extract_pak.py "<DoubaoWork>/app/local_webcontents/biz/biz.pak" .recon/extract
# 2) 还原 source map
python tools/extract_sources.py .recon/extract .recon/src --stats
```

结论性发现请写进 `docs/recon.md`，并附上「怎么验证的」，方便下一个人复现。

## 代码风格

- 注释统一中文，解释**为什么**（尤其是「必须这样否则上游报错」的坑）。
- 错误信息面向使用者，带上下文（账号 / 端点 / 上游错误码）。
- 不引入第三方依赖；确有必要请在 PR 里说明理由。
