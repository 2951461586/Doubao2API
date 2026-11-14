# Doubao2API 常用任务（纯 Go 标准库，无第三方依赖）
#
#   make check     # CI 同款：fmt 校验 + vet + race 测试
#   make cover     # 覆盖率报告
#   make probe ARGS='-path /samantha/skill/list -body {}'
#
# 没有 make 时等价于 `go build ./... && go vet ./... && go test -race ./...`。

BINARY   := doubao2api
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  :=
PKGS     := ./...

.PHONY: all build run fmt fmt-check vet test race cover cover-gate fuzz check docker probe dist release clean help

all: check build

build: ## 编译到当前目录（注入版本号）
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

run: ## 本机运行（默认 127.0.0.1:10086）
	go run .

fmt: ## 格式化
	gofmt -w .

fmt-check: ## 只检查格式（CI 用）
	@sh scripts/fmt-check.sh

vet: ## 静态检查
	go vet $(PKGS)

test: ## 单元测试
	go test $(PKGS)

race: ## 并发回归（CI 必跑）
	go test -race -count=1 $(PKGS)

cover: ## 覆盖率（生成 coverage.out 与 HTML）
	go test -count=1 -coverprofile=coverage.out $(PKGS)
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html
	@echo "报告：coverage.html"

cover-gate: ## 覆盖率门槛（CI 同款）
	go test -count=1 -coverprofile=coverage.out $(PKGS)
	@sh scripts/coverage-gate.sh coverage.out

fuzz: ## SSE 分帧器模糊测试（默认 30 秒）
	go test ./internal/doubao -run '^$$' -fuzz FuzzReadSSE -fuzztime 30s

check: fmt-check vet race ## CI 同款校验

docker: ## 构建镜像
	docker build -t $(BINARY):$(VERSION) .

probe: ## 上游探测（ARGS 透传给 cmd/probe）
	go run ./cmd/probe $(ARGS)

dist: ## 交叉编译发布产物到 dist/（版本号来自 git describe）
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-$(VERSION)-windows-amd64.exe .
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-$(VERSION)-linux-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-$(VERSION)-darwin-arm64 .
	@ls -lh dist

release: ## 打发布 tag（用法: make release VERSION=v0.1.0）
	@sh scripts/release.sh $(VERSION)

clean: ## 清理构建与测试产物
	rm -f $(BINARY) $(BINARY).exe coverage.out coverage.html
	rm -rf dist
	go clean -testcache

help: ## 列出全部目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
