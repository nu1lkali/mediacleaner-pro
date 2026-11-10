#!/bin/sh
# 容器入口：处理 PUID/PGID 权限后降权启动。
#
# 群晖上最常见的坑是"文件属主对不上，程序无法移动文件"。这里在启动时
# 按 PUID/PGID 调整 /data /config /cache 的属主，再以该身份运行，
# 避免把整个容器跑成 root。
#
# 目录布局（宿主机目录由 compose 的 MC_DATA_DIR 决定）：
#   /data     SQLite 库与索引
#   /config   运行配置、initial-admin.txt
#   /cache    隔离区、缩略图、转码代理、导出文件
set -eu

log() { echo "[entrypoint] $*"; }

# 没有额外参数时默认启动主程序
if [ "$#" -eq 0 ]; then
    set -- /app/mediacleaner
fi

PUID="${PUID:-1000}"
PGID="${PGID:-1000}"

# ffmpeg 是视频能力的开关，启动时先自检一次，日志里能直接看出来
if command -v ffmpeg >/dev/null 2>&1; then
    log "ffmpeg: $(ffmpeg -version 2>/dev/null | head -n 1)"
else
    log "警告：未找到 ffmpeg —— 视频相似度/转码/缩略图不可用（完全重复与相似图片不受影响）"
fi
if command -v ffprobe >/dev/null 2>&1; then
    log "ffprobe: $(ffprobe -version 2>/dev/null | head -n 1)"
else
    log "警告：未找到 ffprobe —— 将使用内置元数据解析器"
fi

run_as() {
    exec su-exec "$PUID:$PGID" "$@"
}

# 非 root 启动（compose 里写了 user:）：不再 chown，直接跑
if [ "$(id -u)" != "0" ]; then
    log "以非 root 身份启动（uid=$(id -u) gid=$(id -g)），跳过权限调整"
    exec "$@"
fi

# ---- root 启动：按 PUID/PGID 准备用户与目录 ----
GNAME="$(awk -F: -v g="$PGID" '$3==g{print $1; exit}' /etc/group || true)"
if [ -z "${GNAME:-}" ]; then
    addgroup -g "$PGID" mcapp >/dev/null 2>&1 || true
    GNAME="$(awk -F: -v g="$PGID" '$3==g{print $1; exit}' /etc/group || true)"
fi
[ -n "${GNAME:-}" ] || GNAME=mcapp

UNAME="$(awk -F: -v u="$PUID" '$3==u{print $1; exit}' /etc/passwd || true)"
if [ -z "${UNAME:-}" ]; then
    adduser -S -D -H -u "$PUID" -G "$GNAME" mcapp >/dev/null 2>&1 || true
    UNAME="$(awk -F: -v u="$PUID" '$3==u{print $1; exit}' /etc/passwd || true)"
fi
[ -n "${UNAME:-}" ] || UNAME=mcapp

log "运行身份：${UNAME}(${PUID}):${GNAME}(${PGID})"

# PUID=0 表示以 root 运行：群晖的共享目录常被 ACL 限制，普通用户读不到
# /volume1/xxx。root 可以绕过 ACL，但清理产生的文件属主也会是 root。
if [ "$PUID" = "0" ]; then
    log "注意：以 root 运行（PUID=0）。可访问全部目录，但新建文件属主为 root；"
    log "      想回到普通用户，把 .env 的 PUID/PGID 改回来再部署一次即可。"
fi

# 只在属主不一致时递归 chown：隔离区可能有几十万文件，
# 每次启动都 -R chown 会让容器启动拖到几分钟。
ensure_owner() {
    dir="$1"
    mkdir -p "$dir" 2>/dev/null || true
    cur_u="$(stat -c '%u' "$dir" 2>/dev/null || echo '')"
    cur_g="$(stat -c '%g' "$dir" 2>/dev/null || echo '')"
    if [ "$cur_u" = "$PUID" ] && [ "$cur_g" = "$PGID" ]; then
        return 0
    fi
    chown -R "$PUID:$PGID" "$dir" 2>/dev/null || log "无法调整 $dir 属主（可能是只读挂载或非 root 所有），忽略"
}

# 以目标身份实际试写一个文件：属主对得上不代表可写（目录可能是 555）。
# SQLite 建库失败报的就是 unable to open database file。
can_write() {
    su-exec "$PUID:$PGID" touch "$1/.mc-write-test" >/dev/null 2>&1 || return 1
    rm -f "$1/.mc-write-test" 2>/dev/null || true
    return 0
}

# 保证目录「属主可写 + 可进入」；不可写才递归修，正常路径下 O(1)。
ensure_writable() {
    dir="$1"
    chmod u+rwx "$dir" 2>/dev/null || true
    if can_write "$dir"; then
        return 0
    fi
    log "$dir 不可写（属主 $(stat -c '%U:%G' "$dir" 2>/dev/null)，权限 $(stat -c '%a' "$dir" 2>/dev/null)），正在递归修复…"
    chown -R "$PUID:$PGID" "$dir" 2>/dev/null || true
    chmod -R u+rwX "$dir" 2>/dev/null || true
    if can_write "$dir"; then
        log "$dir 权限已修复"
        return 0
    fi
    log "严重：$dir 仍然不可写，容器无法启动。"
    log "请在 NAS 上执行：sudo chown -R ${PUID}:${PGID} ${MC_HOST_DATA_DIR:-<宿主机数据目录>}"
    log "         并确认目录权限：sudo chmod -R u+rwX ${MC_HOST_DATA_DIR:-<宿主机数据目录>}"
    return 1
}

BAD=0
for d in /data /config /cache; do
    ensure_owner "$d"
    ensure_writable "$d" || BAD=1
done

# 缓存子目录提前建好，省得第一次扫描时同时创建失败
mkdir -p /cache/thumbs /cache/proxy /cache/quarantine /cache/exports 2>/dev/null || true
for d in /cache/thumbs /cache/proxy /cache/quarantine /cache/exports; do
    chown "$PUID:$PGID" "$d" 2>/dev/null || true
    chmod u+rwx "$d" 2>/dev/null || true
done

# 已有数据库文件可能是只读的（例如从别处复制过来），单独放开属主写权限
DBF="${MC_DB:-/data/mediacleaner.db}"
if [ -e "$DBF" ]; then
    chown "$PUID:$PGID" "$DBF" 2>/dev/null || true
    chmod u+rw "$DBF" 2>/dev/null || true
fi
for sfx in -wal -shm -journal; do
    if [ -e "$DBF$sfx" ]; then
        chown "$PUID:$PGID" "$DBF$sfx" 2>/dev/null || true
        chmod u+rw "$DBF$sfx" 2>/dev/null || true
    fi
done

log "数据目录：/data=$(ls -ld /data 2>/dev/null | awk '{print $1,$3":"$4}')"
log "缓存目录：/cache=$(ls -ld /cache 2>/dev/null | awk '{print $1,$3":"$4}')"

# 以目标身份检查预注册的扫描卷能不能读：读不到就在启动日志里说清楚，
# 不用等用户点进目录选择器才发现 permission denied。
probe_roots() {
    list="${MC_ROOTS:-}"
    [ -n "$list" ] || return 0
    OLDIFS="$IFS"
    IFS=';'
    for item in $list; do
        IFS="$OLDIFS"
        p="${item#*=}"
        [ -n "$p" ] || continue
        if [ ! -d "$p" ]; then
            log "提示：扫描卷不存在或未挂载：$p"
            IFS=';'
            continue
        fi
        if su-exec "$PUID:$PGID" sh -c 'test -r "$1" && test -x "$1"' _ "$p" 2>/dev/null; then
            log "扫描卷可读：$p"
        else
            PERM="$(stat -c '%a %U:%G' "$p" 2>/dev/null)"
            if [ "$PUID" = "0" ]; then
                log "警告：扫描卷不可读：$p（$PERM），已以 root 运行仍无法读取"
                log "      处理方式：确认该目录确实挂载进容器，并在 DSM 共享文件夹里开读权限（群晖 ACL 可能连 root 一起限制）"
            else
                log "警告：扫描卷不可读：$p（$PERM），运行身份 ${PUID}:${PGID} 无权访问"
                log "      处理方式：把 .env 的 PUID/PGID 改成该目录属主，或用 --run-as-root 重新部署"
            fi
        fi
        IFS=';'
    done
    IFS="$OLDIFS"
    return 0
}

probe_roots

if [ "$BAD" = "1" ]; then
    log "启动中止：数据目录不可写。修好宿主机目录权限后重启容器即可。"
    exit 1
fi

run_as "$@"
