#!/usr/bin/env bash
#
# MediaCleaner Pro —— 群晖（DSM 6/7，x86_64）一键编译 + 部署
#
# 目录约定：源码与数据彻底分开
#   源码目录：本脚本所在仓库（例如 /volume1/docker/mediacleaner）
#   数据目录：/volume1/docker/mediacleaner-data
#             ├─ data     SQLite 库与索引
#             ├─ config   配置与 initial-admin.txt
#             └─ cache    隔离区 / 缩略图 / 转码代理 / 导出文件
#
# 用法：
#   sudo bash deploy/synology-deploy.sh
#   sudo bash deploy/synology-deploy.sh --rebuild
#   sudo bash deploy/synology-deploy.sh --no-cache
#   sudo bash deploy/synology-deploy.sh --no-build
#   sudo bash deploy/synology-deploy.sh --media /volume1/photo,/volume1/video
#   sudo bash deploy/synology-deploy.sh --no-host-root
#   sudo bash deploy/synology-deploy.sh --run-as-root
#   sudo bash deploy/synology-deploy.sh --readonly
#   sudo bash deploy/synology-deploy.sh --data-dir /volume2/mc-data
#   sudo bash deploy/synology-deploy.sh --port 8088
#   sudo bash deploy/synology-deploy.sh --arch arm64
#   sudo bash deploy/synology-deploy.sh --status
#   sudo bash deploy/synology-deploy.sh --logs
#   sudo bash deploy/synology-deploy.sh --down
#
#   # 基础镜像拉取失败 / 镜像源限流时：
#   sudo bash deploy/synology-deploy.sh --rebuild --pull
#   sudo bash deploy/synology-deploy.sh --rebuild --mirrors "docker.1ms.run;hub.rat.dev"
#
# 默认行为：
#   1. 端口 8083
#   2. 把宿主机根目录挂载进容器（/ → /host），这样界面的目录选择器
#      可以直接浏览 /volume1、/volume2 等真实路径，不用逐个挂载。
#      不需要时可加 --no-host-root，改用 /media/<目录> 精确挂载。
#   3. 构建前自动预拉取 node / golang / alpine 三个基础镜像，
#      依次尝试多个公共镜像源，成功后在本地打回标准标签，
#      避免构建阶段因某个免费节点限流（403 toomanyrequests）而失败。
#   4. 构建走 Docker 层缓存：改 Go 代码只重编后端那一层（约 1-3 分钟），
#      只有首次构建或显式加 --no-cache 才会重跑 npm ci / go mod download。
#
set -eu

# ---------- 路径配置 ----------

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

APP_NAME="mediacleaner"
IMAGE_NAME="mediacleaner:local"

# 独立数据目录（可通过 --data-dir 覆盖）
DATA_ROOT="/volume1/docker/mediacleaner-data"

ENV_FILE="$ROOT/.env"
OVERRIDE_FILE="$ROOT/docker-compose.override.yml"

ACTION="install"
FORCE_BUILD=0
SKIP_BUILD=0
# 1 = 构建时加 --no-cache（全量重建）。默认 0，走层缓存。
NO_CACHE=0
MEDIA_ARG=""
MEDIA_RO=0
ARCH_ARG=""
PORT_ARG=""
# 1 = 把宿主机 / 挂到容器 /host（默认），0 = 只挂载显式媒体目录到 /media
HOST_ROOT=1
HOST_MOUNT="/host"

# 1 = 容器以 root 运行（PUID=0/PGID=0）：群晖共享目录被 ACL 锁住时的兜底
RUN_AS_ROOT=0

# 基础镜像预拉取
FORCE_PULL=0
SKIP_PREPULL=0
MIRRORS_ARG=""
# 公共镜像源候选（; 分隔）。留空项表示直连 docker.io。
# 任何一个源成功即可，失败会自动换下一个。
DEFAULT_MIRRORS="docker.1ms.run;docker.xuanyuan.me;docker.1panel.live;hub.rat.dev;dockerpull.org;docker.m.daocloud.io;docker.nju.edu.cn"

# ---------- 输出 ----------

if [ -t 1 ]; then
    C_OK=$'\033[32m'
    C_WARN=$'\033[33m'
    C_ERR=$'\033[31m'
    C_B=$'\033[1m'
    C_0=$'\033[0m'
else
    C_OK=""
    C_WARN=""
    C_ERR=""
    C_B=""
    C_0=""
fi

info() { printf '%s\n' "$*"; }
ok() { printf '%s✓%s %s\n' "$C_OK" "$C_0" "$*"; }
warn() { printf '%s!%s %s\n' "$C_WARN" "$C_0" "$*"; }
die() {
    printf '%s✗%s %s\n' "$C_ERR" "$C_0" "$*" >&2
    exit 1
}
step() {
    printf '\n%s▸ %s%s\n' "$C_B" "$*" "$C_0"
}

usage() {
    awk 'NR>1 && /^#/ {sub(/^# ?/,""); print; next} NR>1 && !/^#/ {exit}' "${BASH_SOURCE[0]}"
    exit 0
}

# ---------- 参数 ----------

while [ $# -gt 0 ]; do
    case "$1" in
        --rebuild)
            ACTION="install"
            FORCE_BUILD=1
            ;;
        --no-cache)
            ACTION="install"
            NO_CACHE=1
            ;;
        --no-build)
            SKIP_BUILD=1
            ;;
        --media)
            shift
            MEDIA_ARG="${1:-}"
            ;;
        --media=*)
            MEDIA_ARG="${1#*=}"
            ;;
        --readonly|--ro)
            MEDIA_RO=1
            ;;
        --no-host-root)
            HOST_ROOT=0
            ;;
        --host-root)
            HOST_ROOT=1
            ;;
        --run-as-root)
            RUN_AS_ROOT=1
            ;;
        --data-dir)
            shift
            DATA_ROOT="${1:-}"
            ;;
        --data-dir=*)
            DATA_ROOT="${1#*=}"
            ;;
        --port)
            shift
            PORT_ARG="${1:-}"
            ;;
        --port=*)
            PORT_ARG="${1#*=}"
            ;;
        --arch)
            shift
            ARCH_ARG="${1:-}"
            ;;
        --arch=*)
            ARCH_ARG="${1#*=}"
            ;;
        --pull)
            FORCE_PULL=1
            ;;
        --no-prepull)
            SKIP_PREPULL=1
            ;;
        --mirrors)
            shift
            MIRRORS_ARG="${1:-}"
            ;;
        --mirrors=*)
            MIRRORS_ARG="${1#*=}"
            ;;
        --status)
            ACTION="status"
            ;;
        --logs)
            ACTION="logs"
            ;;
        --down)
            ACTION="down"
            ;;
        -h|--help)
            usage
            ;;
        *)
            die "未知参数：$1（使用 --help 查看帮助）"
            ;;
    esac
    shift
done

[ -n "$DATA_ROOT" ] || die "数据目录不能为空"
case "$DATA_ROOT" in
    /*) ;;
    *) die "数据目录必须是绝对路径：$DATA_ROOT" ;;
esac
DATA_ROOT="${DATA_ROOT%/}"

DATA_DIR="$DATA_ROOT/data"
CONFIG_DIR="$DATA_ROOT/config"
CACHE_DIR="$DATA_ROOT/cache"

# ---------- 环境检查 ----------

need_cmd() {
    command -v "$1" >/dev/null 2>&1
}

[ -f "$ROOT/Dockerfile" ] || die "没有找到 Dockerfile（请在仓库根目录执行本脚本）"

step "检查运行环境"

need_cmd docker || die "未找到 Docker，请先在「套件中心」安装 Container Manager"

docker info >/dev/null 2>&1 || die "Docker 守护进程不可用：请用 sudo 执行，或把当前用户加入 docker 组"

if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
elif need_cmd docker-compose; then
    COMPOSE="docker-compose"
else
    die "没有找到 Docker Compose，请在 Container Manager 中启用"
fi

ok "Docker: $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
ok "Compose: $($COMPOSE version --short 2>/dev/null || echo ok)"

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)
        ok "架构：$ARCH（x86_64，原生构建，无需模拟）"
        ;;
    aarch64|arm64)
        warn "架构：$ARCH（ARM64，首次构建耗时较长）"
        ;;
    *)
        warn "未验证的架构：$ARCH"
        ;;
esac

# 交叉构建时显式指定目标平台；同架构原生构建时不设置，避免 0.0.0.0 之外的多余变量
if [ -n "$ARCH_ARG" ]; then
    case "$ARCH_ARG" in
        amd64|x86_64)
            export DOCKER_DEFAULT_PLATFORM=linux/amd64
            ;;
        arm64|aarch64)
            export DOCKER_DEFAULT_PLATFORM=linux/arm64
            ;;
        *)
            die "不支持的架构：$ARCH_ARG（可选 amd64 / arm64）"
            ;;
    esac
    ok "目标平台：$DOCKER_DEFAULT_PLATFORM"
fi

# ---------- 创建独立数据目录 ----------

step "准备独立数据目录（与源码分离）"

if ! mkdir -p "$DATA_DIR" "$CONFIG_DIR" "$CACHE_DIR" 2>/dev/null; then
    die "无法创建 $DATA_ROOT：请用 sudo 执行，或换个可写目录（--data-dir）"
fi

[ -w "$DATA_DIR" ] || die "$DATA_DIR 不可写，请检查权限或用 sudo 执行"

ok "数据根目录：$DATA_ROOT"
ok "SQLite 数据：$DATA_DIR"
ok "配置文件：$CONFIG_DIR"
ok "缓存及隔离区：$CACHE_DIR"

# 老版本把数据放在源码目录里，绝不自动删除，只提示
for legacy in data config cache; do
    if [ -d "$ROOT/$legacy" ] && [ -n "$(ls -A "$ROOT/$legacy" 2>/dev/null || true)" ]; then
        warn "源码目录里仍有旧数据：$ROOT/$legacy"
        warn "不会自动迁移或删除；如需保留旧库，请先停止容器后手工复制到 $DATA_ROOT"
    fi
done

# ---------- 用户权限 ----------

CUR_UID="$(id -u)"
CUR_GID="$(id -g)"

if [ "$CUR_UID" = "0" ]; then
    DEF_PUID="${PUID:-1026}"
    DEF_PGID="${PGID:-100}"
else
    DEF_PUID="$CUR_UID"
    DEF_PGID="$CUR_GID"
fi

# ---------- 环境配置 ----------

step "准备环境配置"

rand_pwd() {
    if need_cmd openssl; then
        openssl rand -base64 24 2>/dev/null | tr -dc 'A-Za-z0-9' | cut -c1-16 || true
    else
        tr -dc 'A-Za-z0-9' < /dev/urandom 2>/dev/null | cut -c1-16 || true
    fi
}

# set_env KEY VALUE：存在则替换，不存在则追加（.env 是唯一配置来源）
set_env() {
    key="$1"
    val="$2"
    [ -f "$ENV_FILE" ] || : > "$ENV_FILE"
    if grep -q "^${key}=" "$ENV_FILE" 2>/dev/null; then
        awk -v k="$key" -v v="$val" 'BEGIN{FS=OFS="="}
            $1==k {print k"="v; next} {print}' "$ENV_FILE" > "$ENV_FILE.tmp"
        mv "$ENV_FILE.tmp" "$ENV_FILE"
    else
        printf '%s=%s\n' "$key" "$val" >> "$ENV_FILE"
    fi
}

get_env() {
    key="$1"
    [ -f "$ENV_FILE" ] || return 1
    grep "^${key}=" "$ENV_FILE" 2>/dev/null | head -n 1 | cut -d= -f2- || return 1
}

if [ -f "$ENV_FILE" ]; then
    ok ".env 已存在，保留现有配置"
else
    GEN_PWD="$(rand_pwd)"
    [ -n "$GEN_PWD" ] || GEN_PWD="mediacleaner2024"

    cat > "$ENV_FILE" <<EOF
# MediaCleaner Pro 运行配置（由 deploy/synology-deploy.sh 生成）
MC_PORT=8083
TZ=Asia/Shanghai
PUID=$DEF_PUID
PGID=$DEF_PGID
MC_ADMIN_PASSWORD=$GEN_PWD
MC_THREADS=4
MC_QUARANTINE_DAYS=30
MC_SESSION_HOURS=168
MC_MEM_LIMIT=2g
GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
EOF

    chmod 600 "$ENV_FILE" 2>/dev/null || true
    ok ".env 已生成"
fi

# 数据目录与端口写入 .env，compose 直接读取
set_env MC_DATA_DIR "$DATA_ROOT"
if [ -n "$PORT_ARG" ]; then
    set_env MC_PORT "$PORT_ARG"
fi

MC_PORT_VALUE="$(get_env MC_PORT || echo 8083)"
[ -n "$MC_PORT_VALUE" ] || MC_PORT_VALUE=8083

# 老版本默认 8080，这里跟随新默认值升级一次；想保持原端口用 --port 8080 显式指定
if [ -z "$PORT_ARG" ] && [ "$MC_PORT_VALUE" = "8080" ]; then
    set_env MC_PORT 8083
    MC_PORT_VALUE=8083
    warn "检测到旧默认端口 8080，已切换为 8083（要保留 8080 请用 --port 8080 重新部署）"
fi

ok "端口：$MC_PORT_VALUE"

# 运行身份：群晖的共享目录常被 ACL 限制，普通用户读不到 /volume1/xxx，
# 目录选择器就会报 permission denied。--run-as-root 是这种场景的兜底。
if [ "$RUN_AS_ROOT" = "1" ]; then
    set_env PUID 0
    set_env PGID 0
    warn "容器将以 root 运行（PUID=0/PGID=0）：可读全部目录，但清理新建的文件属主为 root"
else
    CUR_PUID="$(get_env PUID || echo '')"
    if [ "$CUR_PUID" = "0" ]; then
        info "当前 .env 仍是 PUID=0（root 运行）；要改回普通用户，改 PUID/PGID 后重新部署"
    fi
fi

# 允许注册/浏览的根：勾选了宿主机根挂载时把 /host 一并放进白名单
if [ "$HOST_ROOT" = "1" ]; then
    set_env MC_ALLOW_PATHS "/host;/media"
else
    set_env MC_ALLOW_PATHS "/media"
fi

# ---------- 媒体目录 ----------

step "准备媒体目录挂载"

MEDIA_LIST="$MEDIA_ARG"

if [ -z "$MEDIA_LIST" ]; then
    MEDIA_LIST="$(get_env MC_MEDIA || echo '')"
fi

if [ -z "$MEDIA_LIST" ]; then
    DETECTED=""
    for base in /volume1 /volume2 /volume3 /volume4; do
        [ -d "$base" ] || continue
        for name in photo photos video videos movie movies music media; do
            [ -d "$base/$name" ] || continue
            DETECTED="${DETECTED:+$DETECTED,}$base/$name"
        done
    done
    MEDIA_LIST="$DETECTED"
    if [ -n "$MEDIA_LIST" ]; then
        info "自动探测到媒体目录：$MEDIA_LIST"
    else
        warn "没有自动探测到媒体目录；稍后可在「系统设置 → 存储卷」里手动添加"
    fi
fi

# 记住媒体清单，下次 --rebuild 时不用重新传参
if [ -n "$MEDIA_LIST" ]; then
    set_env MC_MEDIA "$MEDIA_LIST"
fi

if [ "$MEDIA_RO" = "1" ]; then
    MOUNT_OPT=":ro"
    MOUNT_DESC="只读"
else
    MOUNT_OPT=""
    MOUNT_DESC="读写"
fi

# 容器内挂载点：/media/<去掉 /volumeN/ 并把 / 换成 _>
container_target() {
    printf '%s' "$1" | sed 's#^/volume[0-9]*/##; s#/#_#g'
}

# ---------- 生成 Compose Override ----------

step "生成 Docker Compose 配置"

# 数据目录由 docker-compose.yml 通过 MC_DATA_DIR 处理，这里不重复声明，
# 只写：① 宿主机根挂载（可选）② 精确媒体挂载（关闭根挂载时）。
MOUNTS_TEXT=""
ROOTS_TEXT=""

if [ "$HOST_ROOT" = "1" ]; then
    MOUNTS_TEXT="      - \"/:$HOST_MOUNT$MOUNT_OPT\"\n"
    ok "宿主机根目录：/ → $HOST_MOUNT（$MOUNT_DESC）"
    warn "已挂载宿主机根目录：界面上可以浏览并选择 /volume1 等真实路径，"
    warn "但容器也因此能看到整机文件。只想开放媒体目录时用 --no-host-root。"
fi

OLDIFS="$IFS"
IFS=','
for p in $MEDIA_LIST; do
    IFS="$OLDIFS"
    p="$(printf '%s' "$p" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    [ -n "$p" ] || continue
    case "$p" in
        /*) ;;
        *) warn "非绝对路径，跳过：$p"; continue ;;
    esac
    if [ ! -d "$p" ]; then
        warn "目录不存在，跳过：$p"
        continue
    fi

    if [ "$HOST_ROOT" = "1" ]; then
        # 根挂载已覆盖，容器内路径就是 /host + 宿主路径
        target="$HOST_MOUNT$p"
        ROOTS_TEXT="${ROOTS_TEXT:+$ROOTS_TEXT;}$(container_target "$p")=$target"
        ok "注册扫描卷：$p → $target"
    else
        safe="$(container_target "$p")"
        MOUNTS_TEXT="${MOUNTS_TEXT}      - \"$p:/media/$safe$MOUNT_OPT\"\n"
        ROOTS_TEXT="${ROOTS_TEXT:+$ROOTS_TEXT;}$safe=/media/$safe"
        ok "挂载：$p → /media/$safe（$MOUNT_DESC）"
    fi
    IFS=','
done
IFS="$OLDIFS"

TMP_OVERRIDE="${OVERRIDE_FILE}.tmp"

{
    echo "# MediaCleaner Pro 自动生成配置 —— 由 deploy/synology-deploy.sh 维护"
    echo "# 数据目录不在此文件：见 docker-compose.yml 的 MC_DATA_DIR（$DATA_ROOT）"
    echo "# 手工添加媒体挂载请改用 --media 重新生成，否则下次部署会被覆盖。"
    echo "services:"
    echo "  mediacleaner:"
    if [ -n "$MOUNTS_TEXT" ]; then
        echo "    volumes:"
        printf "%b" "$MOUNTS_TEXT"
    else
        echo "    # 暂未挂载任何媒体目录（可用 --media /volume1/photo 指定）"
    fi
} > "$TMP_OVERRIDE"

mv "$TMP_OVERRIDE" "$OVERRIDE_FILE"
ok "Compose Override 已生成"

# ---------- 自动注册媒体卷 ----------

if [ -n "$ROOTS_TEXT" ]; then
    if [ "$(get_env MC_ROOTS || echo '')" != "$ROOTS_TEXT" ]; then
        set_env MC_ROOTS "$ROOTS_TEXT"
        ok "已更新 MC_ROOTS（自动注册的卷默认「允许清理」关闭）"
    fi
elif [ "$HOST_ROOT" = "1" ]; then
    info "未预置扫描卷：登录后在「媒体扫描」页用目录选择器挑 /host 下的任意目录即可"
else
    warn "没有可挂载的媒体目录，扫描前请在界面里添加存储卷"
fi

# ---------- 状态类操作 ----------

case "$ACTION" in
    status)
        step "容器状态"
        $COMPOSE ps || true
        CID="$($COMPOSE ps -q "$APP_NAME" 2>/dev/null || true)"
        if [ -n "$CID" ]; then
            HEALTH="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}n/a{{end}}' "$CID" 2>/dev/null || echo n/a)"
            info "健康状态：$HEALTH"
            info "访问地址：http://$(hostname -i 2>/dev/null | awk '{print $1}'):$MC_PORT_VALUE"
            info "数据目录：$DATA_ROOT"
        fi
        exit 0
        ;;
    logs)
        exec $COMPOSE logs -f --tail=200 "$APP_NAME"
        ;;
    down)
        step "停止并移除容器"
        $COMPOSE down
        ok "容器已停止（数据仍保留在 $DATA_ROOT）"
        exit 0
        ;;
esac

# ---------- 基础镜像预拉取（多镜像源兜底）----------

# 构建用到的三个基础镜像，必须与 Dockerfile 中的 FROM 保持一致。
# 若 .env 里用 MC_NODE_IMAGE 等变量换了源，这里跟着走，保证预拉取的正是构建要用的。
BASE_IMAGES="${MC_NODE_IMAGE:-node:20-alpine} ${MC_GO_IMAGE:-golang:1.23-alpine} ${MC_RUNTIME_IMAGE:-alpine:3.20}"

# 镜像源列表优先级：--mirrors > MC_DOCKER_MIRRORS > 内置候选
MIRROR_LIST="${MIRRORS_ARG:-${MC_DOCKER_MIRRORS:-$DEFAULT_MIRRORS}}"

# 交叉构建时才需要指定平台，原生构建留空
PULL_PLATFORM="${DOCKER_DEFAULT_PLATFORM:-}"

docker_pull_platform() {
    if [ -n "$PULL_PLATFORM" ]; then
        docker pull --platform "$PULL_PLATFORM" "$1"
    else
        docker pull "$1"
    fi
}

# prepull_image node:20-alpine
#   1) 本地已有 → 跳过
#   2) 逐个镜像源拉取 <mirror>/library/<repo>:<tag>，成功后 tag 成标准名
#   3) 全部失败 → 再直连 docker.io 试一次（走 daemon.json 里配的加速器）
prepull_image() {
    full="$1"
    repo="${full%:*}"
    tag="${full##*:}"

    if [ "$FORCE_PULL" = "0" ] && docker image inspect "$full" >/dev/null 2>&1; then
        ok "基础镜像已就绪：$full"
        return 0
    fi

    # 已经是完整引用（带仓库地址，如 docker.1ms.run/library/node:20-alpine）就不再套镜像源
    case "$full" in
        */*)
            info "  拉取 $full"
            if docker_pull_platform "$full" >/dev/null 2>&1; then
                ok "基础镜像已就绪：$full"
                return 0
            fi
            warn "拉取失败：$full"
            return 1
            ;;
    esac

    OLDIFS="$IFS"
    IFS=';'
    for m in $MIRROR_LIST; do
        IFS="$OLDIFS"
        m="$(printf '%s' "$m" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
        [ -n "$m" ] || continue
        m="${m%/}"

        # 已经是完整仓库地址（含 /）就原样用，否则补 /library/
        case "$m" in
            */*) ref="$m/$repo:$tag" ;;
            *) ref="$m/library/$repo:$tag" ;;
        esac

        info "  拉取 $ref"
        if docker_pull_platform "$ref" >/dev/null 2>&1; then
            docker tag "$ref" "$full" 2>/dev/null || true
            ok "基础镜像已就绪：$full  ←  $m"
            IFS="$OLDIFS"
            return 0
        fi
        warn "  $m 不可用，切换下一个源"
        IFS=';'
    done
    IFS="$OLDIFS"

    if docker_pull_platform "$full" >/dev/null 2>&1; then
        ok "基础镜像已就绪：$full  ←  docker.io（daemon 加速器）"
        return 0
    fi

    warn "所有镜像源都没拉下来：$full"
    return 1
}

if [ "$SKIP_BUILD" = "0" ] && [ "$SKIP_PREPULL" = "0" ]; then
    step "预拉取基础镜像（多镜像源兜底）"
    info "镜像源顺序：$(printf '%s' "$MIRROR_LIST" | sed 's/;/  →  /g')"

    PREPULL_FAIL=0
    for img in $BASE_IMAGES; do
        prepull_image "$img" || PREPULL_FAIL=1
    done

    if [ "$PREPULL_FAIL" = "1" ]; then
        warn "有基础镜像未预拉取成功，构建时 Docker 仍会自己再试一次。"
        warn "若再次失败，可指定可用源后重试："
        warn "  sudo bash deploy/synology-deploy.sh --rebuild --mirrors \"docker.1ms.run;hub.rat.dev\""
        warn "或在 Container Manager → 注册表 → 设置 里换一个镜像加速地址。"
    else
        ok "基础镜像全部就绪，构建阶段无需再联网拉取"
    fi
fi

# ---------- 构建镜像 ----------

if [ "$SKIP_BUILD" = "0" ]; then
    step "构建镜像（前端 + Go 二进制 + ffmpeg）"

    # 默认走层缓存：只重编改动过的层。
    # 依赖层（npm ci / go mod download / apk add）在 package-lock、go.sum 不变时直接复用，
    # 通常 1-3 分钟；只有首次或加 --no-cache 才会跑 8-12 分钟。
    BUILD_ARGS=""
    if [ "$NO_CACHE" = "1" ]; then
        BUILD_ARGS="--no-cache"
        warn "全量重建（--no-cache）：npm ci、go mod download、apk add 都会重跑，约 8-12 分钟"
    else
        info "增量构建：未改动的层走缓存（依赖层通常直接复用）"
    fi

    BUILD_START="$(date +%s)"
    # shellcheck disable=SC2086
    if ! $COMPOSE build $BUILD_ARGS; then
        die "镜像构建失败：多为基础镜像拉取被限流。先试 --mirrors 换源，再检查磁盘空间与内存"
    fi
    BUILD_SECS=$(( $(date +%s) - BUILD_START ))
    ok "镜像构建完成（用时 ${BUILD_SECS}s）"

    if [ "$BUILD_SECS" -gt 300 ] && [ "$NO_CACHE" = "0" ]; then
        warn "本次用时偏长：如果只是改了几行 Go 代码，说明前端依赖层被重建了，"
        warn "检查是否动过 frontend/package.json 或 package-lock.json"
    fi
else
    if docker image inspect "$IMAGE_NAME" >/dev/null 2>&1; then
        warn "跳过构建，使用现有镜像 $IMAGE_NAME"
    else
        die "跳过构建，但本地不存在镜像 $IMAGE_NAME；请先去掉 --no-build 构建一次"
    fi
fi

# ---------- 校正数据目录权限 ----------

# 群晖上目录可能被 ACL 改成 555（属主对得上但不可写），此时容器内 SQLite
# 建库会失败并报 unable to open database file。这里在起容器前先摆平。
step "校正数据目录权限"

TARGET_UID="$(get_env PUID || echo "$DEF_PUID")"
TARGET_GID="$(get_env PGID || echo "$DEF_PGID")"
[ -n "${TARGET_UID:-}" ] || TARGET_UID="$DEF_PUID"
[ -n "${TARGET_GID:-}" ] || TARGET_GID="$DEF_PGID"

for d in "$DATA_DIR" "$CONFIG_DIR" "$CACHE_DIR"; do
    cur_owner="$(stat -c '%u:%g' "$d" 2>/dev/null || echo '')"
    if [ "$cur_owner" != "$TARGET_UID:$TARGET_GID" ]; then
        if chown -R "$TARGET_UID:$TARGET_GID" "$d" 2>/dev/null; then
            ok "属主已调整为 $TARGET_UID:$TARGET_GID：$d"
        else
            warn "无法调整属主（可能不是 root）：$d"
        fi
    fi
    chmod u+rwx "$d" 2>/dev/null || warn "无法修改权限：$d"
    ok "$d → $(stat -c '%a %U:%G' "$d" 2>/dev/null || echo '?')"
done

# ---------- 启动容器 ----------

step "启动容器"

$COMPOSE up -d --remove-orphans
ok "容器已启动"

# ---------- 健康检查 ----------

step "等待服务就绪"

CID="$($COMPOSE ps -q "$APP_NAME" 2>/dev/null || true)"
READY=0
i=0
while [ "$i" -lt 60 ]; do
    i=$((i + 1))

    if [ -n "$CID" ]; then
        ST="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}unknown{{end}}' "$CID" 2>/dev/null || echo unknown)"
        case "$ST" in
            healthy)
                READY=1
                break
                ;;
            unhealthy)
                break
                ;;
        esac
    fi

    if need_cmd curl; then
        if curl -fsS "http://127.0.0.1:$MC_PORT_VALUE/api/health" >/dev/null 2>&1; then
            READY=1
            break
        fi
    elif need_cmd wget; then
        if wget -qO- "http://127.0.0.1:$MC_PORT_VALUE/api/health" >/dev/null 2>&1; then
            READY=1
            break
        fi
    fi

    sleep 2
done

if [ "$READY" != "1" ]; then
    warn "健康检查未通过，下面是最近日志："
    $COMPOSE logs --tail=60 "$APP_NAME" || true

    LOGS="$($COMPOSE logs --tail=200 "$APP_NAME" 2>&1 || true)"
    RUN_PUID="$(get_env PUID || echo 1026)"
    RUN_PGID="$(get_env PGID || echo 100)"
    [ -n "${RUN_PUID:-}" ] || RUN_PUID=1026
    [ -n "${RUN_PGID:-}" ] || RUN_PGID=100

    case "$LOGS" in
        *"打开数据库失败"*|*"unable to open database"*|*"仍然不可写"*)
            printf '\n'
            warn "原因：容器内的 /data 无法写入（SQLite 建库失败），不是端口问题。"
            warn "在 NAS 上修一次宿主机目录权限后重启即可："
            info "  sudo chown -R $RUN_PUID:$RUN_PGID $DATA_ROOT"
            info "  sudo chmod -R u+rwX $DATA_ROOT"
            info "  sudo docker compose up -d"
            ;;
        *"address already in use"*|*"failed to bind"*|*"bind: address"*)
            printf '\n'
            warn "原因：端口 $MC_PORT_VALUE 已被占用。"
            info "  换端口重跑：sudo bash deploy/synology-deploy.sh --no-build --port 8093"
            ;;
        *)
            printf '\n'
            warn "按日志排查：目录权限、端口占用、内存上限（MC_MEM_LIMIT=$(get_env MC_MEM_LIMIT || echo 2g)）。"
            ;;
    esac

    die "启动失败"
fi

ok "服务健康检查通过"

# ---------- 完成信息 ----------

ADMIN_PWD="$(get_env MC_ADMIN_PASSWORD || echo '')"
if [ -z "$ADMIN_PWD" ] && [ -f "$CONFIG_DIR/initial-admin.txt" ]; then
    ADMIN_PWD="$(grep -oE '(密码|password)[:：].*' "$CONFIG_DIR/initial-admin.txt" 2>/dev/null | sed 's/.*[:：]//' || true)"
fi

HOST_IP="$(ip route get 1.1.1.1 2>/dev/null | awk '{print $7; exit}' || true)"
[ -n "${HOST_IP:-}" ] || HOST_IP="$(hostname -i 2>/dev/null | awk '{print $1}')"
[ -n "${HOST_IP:-}" ] || HOST_IP="NAS_IP"

printf '\n%s============================================%s\n' "$C_B" "$C_0"
info "MediaCleaner Pro 部署完成"
printf '%s============================================%s\n' "$C_B" "$C_0"
info "访问地址：http://$HOST_IP:$MC_PORT_VALUE"
info "账号：admin"
info "初始密码：${ADMIN_PWD:-见 $CONFIG_DIR/initial-admin.txt}"

printf '\n'
info "源码目录：$ROOT"
info "数据目录：$DATA_ROOT"
info "数据库：$DATA_DIR"
info "配置：$CONFIG_DIR"
info "缓存：$CACHE_DIR"
info "隔离区：$CACHE_DIR/quarantine"

printf '\n'
info "后续三步："
info "1. 登录后在「系统设置 → 存储卷」确认卷已注册（允许清理默认关闭）"
info "2. 「媒体扫描」选「极速」跑一次，确认索引与分组正常"
info "3. 核对无误后再打开「允许清理」，然后创建清理任务"

printf '\n'
info "常用命令："
info "  bash deploy/synology-deploy.sh --status     # 查看状态"
info "  bash deploy/synology-deploy.sh --logs       # 跟踪日志"
info "  bash deploy/synology-deploy.sh --rebuild    # 改完代码后重建"
info "  bash deploy/synology-deploy.sh --down       # 停止（数据保留）"
printf '\n'
