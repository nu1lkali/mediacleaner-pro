#
# MediaCleaner Pro —— 三阶段构建
#   1. web   : 编译 React 前端为静态资源
#   2. api   : 编译 Go 后端为静态二进制（CGO_ENABLED=0，纯 Go SQLite）
#   3. final : Alpine 运行时，内置 ffmpeg（视频关键帧/转码/缩略图都靠它）
#
# 群晖上直接 `docker compose build` 即可，不依赖私有镜像仓库，
# 也不依赖 dockerfile 前端镜像（DSM 的 Docker 不一定能拉到 # syntax 镜像）。
#
# 数据目录不参与构建：容器内的 /data /config /cache 由 compose 挂载宿主机目录，
# 源码目录只用来构建镜像。

ARG NODE_IMAGE=node:20-alpine
ARG GO_IMAGE=golang:1.23-alpine
ARG RUNTIME_IMAGE=alpine:3.20

# ---------- 1. 前端 ----------
FROM ${NODE_IMAGE} AS web
WORKDIR /src/frontend

# 国内网络优先走 npmmirror；换源只在构建层生效，不影响运行镜像
ARG NPM_REGISTRY=https://registry.npmmirror.com
RUN npm config set registry "$NPM_REGISTRY" \
    && npm config set fetch-retries 5 \
    && npm config set fetch-retry-maxtimeout 60000

# 先只拷依赖清单，最大化利用层缓存
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY frontend/ ./
RUN npm run build

# 构建自检：Tailwind 依赖 postcss.config.js，缺文件时会「构建成功但没有样式」。
# 这里在镜像里直接拦下来，避免又部署出一个白板页面。
RUN set -e; \
    ls dist/assets/*.css >/dev/null 2>&1 || { echo "构建产物缺少 CSS（dist/assets/*.css）" >&2; exit 1; }; \
    if grep -q "@tailwind" dist/assets/*.css; then \
        echo "CSS 未经过 Tailwind 编译：@tailwind 指令被原样输出，请检查 frontend/postcss.config.js" >&2; \
        exit 1; \
    fi; \
    echo "前端构建自检通过"

# ---------- 2. 后端 ----------
FROM ${GO_IMAGE} AS api
WORKDIR /src/backend

# 国内网络优先走镜像；失败自动回落官方源
ARG GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
ARG APP_VERSION=1.1.0
ARG TARGETARCH
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOFLAGS=-mod=mod \
    GOPROXY=${GOPROXY}

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./
RUN GOARCH=${TARGETARCH:-amd64} go build -trimpath \
        -ldflags "-s -w -X mediacleaner/internal/config.Version=${APP_VERSION}" \
        -o /out/mediacleaner ./cmd/server

# ---------- 3. 运行时 ----------
FROM ${RUNTIME_IMAGE}

# ffmpeg 一并装进镜像：不依赖 NAS 宿主机再装第三方套件
RUN apk add --no-cache \
        ffmpeg \
        tzdata \
        ca-certificates \
        su-exec \
    && rm -rf /var/cache/apk/*

# 容器内的数据根：/data 放库与索引，/config 放配置，/cache 放隔离区与缩略图。
# 宿主机上的真实位置由 compose 的 MC_DATA_DIR 决定（默认
# /volume1/docker/mediacleaner-data），与源码目录完全分开。
ENV MC_HOME=/app \
    MC_DATA_ROOT=/data \
    MC_ADDR=:8080 \
    MC_CACHE_DIR=/cache \
    MC_CONFIG_DIR=/config \
    MC_DB=/data/mediacleaner.db \
    MC_ALLOW_PATHS=/media \
    MC_FFMPEG=/usr/bin/ffmpeg \
    MC_FFPROBE=/usr/bin/ffprobe \
    TZ=Asia/Shanghai

WORKDIR /app
COPY --from=api /out/mediacleaner /app/mediacleaner
COPY --from=web /src/frontend/dist /app/frontend/dist
COPY deploy/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && mkdir -p /data /config /cache

# 数据、配置、缓存都落在挂载卷里；媒体目录由 compose 单独挂载
VOLUME ["/data", "/config", "/cache"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -q --spider http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
