# ---- 构建阶段 -------------------------------------------------------------
# 网关为纯 Go 标准库实现（go.mod 无 require），构建阶段不需要联网拉依赖，
# 运行阶段也可以保持极简。
FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod ./
COPY main.go ./
COPY internal ./internal

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/doubao2api .

# ---- 运行阶段 -------------------------------------------------------------
FROM alpine:3.20

# ca-certificates: 访问 www.doubao.com 需要 TLS 根证书
# tzdata:          正确的本地时间（日志与统计按本地时区）
# wget:            HEALTHCHECK 探针（busybox 自带）
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S -g 10001 doubao \
 && adduser -S -G doubao -u 10001 -h /home/doubao doubao \
 && mkdir -p /data \
 && chown -R 10001:10001 /data

COPY --from=build /out/doubao2api /usr/local/bin/doubao2api

USER 10001:10001

ENV DOUBAO_HOST=0.0.0.0 \
    DOUBAO_PORT=10086 \
    DOUBAO_DATA_PATH=/data/doubao2api-data.json \
    TZ=Asia/Shanghai

VOLUME ["/data"]
EXPOSE 10086

# 用 exec 形式，不依赖容器内的 shell；端口与 EXPOSE 保持一致
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["wget", "-qO-", "http://127.0.0.1:10086/ping"]

ENTRYPOINT ["/usr/local/bin/doubao2api"]
