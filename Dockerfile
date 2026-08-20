# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# 1. 前端
#
# 必须先于 Go 构建：后端用 //go:embed all:dist 把前端产物编进二进制，
# 那个目录不存在的话 Go 直接编译失败（embed 的路径在编译期解析）。
# ---------------------------------------------------------------------------
FROM node:24-alpine AS web

WORKDIR /src/web
# 先只拷依赖清单：改业务代码时这一层还能命中缓存，不必重装依赖。
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
# vite.config.ts 的 outDir 指向 ../internal/web/dist，产物会落在 /src/internal/web/dist
RUN npm run build


# ---------------------------------------------------------------------------
# 2. 后端
# ---------------------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 前端产物来自上一阶段。宿主机上的 internal/web/dist 已被 .dockerignore 排除，
# 免得把本地那份陈旧产物带进镜像。
COPY --from=web /src/internal/web/dist ./internal/web/dist

# CGO_ENABLED=0：SQLite 用的是 modernc.org/sqlite，纯 Go 实现，
# 不需要 libc，因此能编出完全静态的二进制。
# -trimpath 去掉构建机的绝对路径，-s -w 去掉调试符号。
# 版本注到 main.version：httpapi.Version 在 run() 里会被 main.version 覆盖。
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/ocicore ./cmd/server


# ---------------------------------------------------------------------------
# 3. 运行时
#
# 选 alpine 而不是 scratch/distroless：这是个自托管面板，出问题时人得能
# exec 进去看一眼。多出来的几 MB 换一个可排查的运行环境，值。
# ---------------------------------------------------------------------------
FROM alpine:3.21

# ca-certificates：调用 Oracle 的 HTTPS 接口要验证证书链，没有它一律握手失败。
# tzdata：面板显示的是本地时间，容器默认 UTC，日志与「N 分钟前」会整体偏几小时。
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -g 10001 -S ocicore \
    && adduser -u 10001 -S -G ocicore -h /app ocicore

WORKDIR /app
COPY --from=build /out/ocicore /usr/local/bin/ocicore

# 数据目录。master.key 与加密后的 OCI 私钥都在这里，必须挂成持久卷——
# 丢了这个目录，所有已保存的账号私钥都解不开，只能重新添加。
RUN mkdir -p /app/data && chown -R ocicore:ocicore /app
VOLUME ["/app/data"]

USER ocicore

# 默认监听 127.0.0.1，在容器里等于谁也连不上——必须显式绑到 0.0.0.0。
# 这不等于把面板暴露到公网：容器内的 0.0.0.0 仍受 docker 的端口映射约束，
# 对外暴露与否由 -p 决定。
ENV OCICORE_ADDR=0.0.0.0:8080 \
    OCICORE_DATA_DIR=/app/data \
    TZ=Asia/Shanghai

EXPOSE 8080

# /api/status 不需要登录，正好用来探活。
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/api/status >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/ocicore"]
