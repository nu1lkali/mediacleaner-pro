#!/usr/bin/env python
"""把本地源码增量同步到 NAS SMB 共享，带内容规范化与 SHA256 校验。

用法：
    python tools/nas_sync.py                 # dry-run：只列差异，不写任何文件
    python tools/nas_sync.py --apply         # 实际同步，并逐个回读校验
    python tools/nas_sync.py --nas //host/share

约定：
  * 只推本地存在且白名单内的源码/配置/文档；NAS 独有文件一律不删、不改。
  * 文本文件先把 CRLF 统一成 LF 再比较，避免 Windows 行尾造成虚假差异；
    写入 NAS 时统一 LF（shell 脚本 / Dockerfile / compose 必须是 LF）。
  * 二进制文件按原始字节比较与复制。
  * 复制后立即回读 SHA256，与期望字节比对，不一致立即报错退出。
"""
import argparse
import hashlib
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_NAS = "//192.168.2.124/docker/MediaCleaner"

TOP_FILES = [
    "README.md", "Dockerfile", ".dockerignore", "docker-compose.yml",
    "docker-compose.override.yml", "build.sh", "build.ps1",
]
TREES = ["backend", "frontend", "deploy", "tools"]

# 按「目录名」整棵剪枝。注意不要往这里加 "config"：
# backend/internal/config 是源码目录，按名字排除会让 config.go 永远同步不过去，
# NAS 上编译 quarantine.go 时就会报 cfg.HostRoot undefined。
# 运行期的 data/ cache/ config/ 都在仓库根目录，不在 TREES 里，本来就不会被遍历到。
EXCLUDE_DIRS = {
    "node_modules", ".tools", "testdata", "bin", "app", "tests", "dist",
    "__pycache__", ".git", ".idea", ".vscode", ".workbuddy", ".env.local",
}
EXCLUDE_EXT = {
    ".log", ".db", ".db-shm", ".db-wal", ".db-journal", ".exe", ".zip",
    ".tar", ".gz", ".pyc", ".pyo", ".tmp", ".swp",
}
# 永不推送：NAS 上的运行期/环境相关文件，推过去会破坏部署
SKIP_FILES = {
    "docker-compose.override.yml": "NAS 由部署脚本生成，含 \"/:/host\" 根挂载；本地仅空占位，推送会丢失扫描范围",
    "backend/go.sum": "NAS 版本已可构建；本轮未改 go.mod，本地多出的校验和只是本地 go vet 追加",
    "tools/_runcheck.py": "本地一次性检查脚手架，NAS 上无用",
}

# 明确按文本处理（即便内容为空也统一 LF）
TEXT_NAMES = {"Dockerfile", ".dockerignore", "build.sh"}
TEXT_EXT = {
    ".go", ".mod", ".sum", ".ts", ".tsx", ".js", ".jsx", ".json", ".css",
    ".html", ".md", ".yml", ".yaml", ".sh", ".env", ".conf", ".toml",
    ".sql", ".py", ".txt", ".svg", ".gitignore", ".cfg", ".ini",
}


def is_text(rel: str, data: bytes) -> bool:
    p = Path(rel)
    if p.name in TEXT_NAMES or p.suffix.lower() in TEXT_EXT:
        return True
    if p.suffix.lower() in EXCLUDE_EXT:
        return False
    if b"\x00" in data:
        return False
    try:
        data.decode("utf-8")
        return True
    except UnicodeDecodeError:
        return False


def norm(data: bytes) -> bytes:
    return data.replace(b"\r\n", b"\n")


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def local_files() -> list[tuple[str, Path]]:
    out: list[tuple[str, Path]] = []
    for name in TOP_FILES:
        if name in SKIP_FILES:
            continue
        p = ROOT / name
        if p.is_file():
            out.append((name, p))
    for tree in TREES:
        base = ROOT / tree
        if not base.is_dir():
            continue
        for dirpath, dirnames, filenames in os.walk(base):
            d = Path(dirpath)
            dirnames[:] = [x for x in dirnames
                           if x not in EXCLUDE_DIRS and not x.startswith(".")]
            for fn in filenames:
                f = d / fn
                if fn.startswith(".") and fn not in {".dockerignore", ".env"}:
                    continue
                if f.suffix.lower() in EXCLUDE_EXT:
                    continue
                rel = f.relative_to(ROOT).as_posix()
                if rel in SKIP_FILES:
                    continue
                out.append((rel, f))
    return sorted(set(out), key=lambda t: t[0])


def plan(nas: Path) -> list[tuple[str, Path, Path, bytes, str]]:
    """返回 [(rel, 本地路径, NAS路径, 期望写入字节, 原因)]"""
    items = []
    for rel, src in local_files():
        raw = src.read_bytes()
        expected = norm(raw) if is_text(rel, raw) else raw
        dst = nas / rel
        if not dst.is_file():
            items.append((rel, src, dst, expected, "NEW"))
            continue
        cur = dst.read_bytes()
        same = (norm(cur) == expected) if is_text(rel, raw) else (cur == raw)
        if same:
            continue
        items.append((rel, src, dst, expected, "CHANGED"))
    return items


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--apply", action="store_true", help="实际写入（默认仅 dry-run）")
    ap.add_argument("--nas", default=DEFAULT_NAS)
    args = ap.parse_args()

    nas = Path(args.nas)
    if not nas.is_dir():
        print(f"NAS 不可达：{nas}")
        return 2

    print("保护名单（不推送）：")
    for rel, why in SKIP_FILES.items():
        print(f"  {rel}  <- {why}")
    print()

    items = plan(nas)
    if not items:
        print("本地与 NAS 完全一致，无需同步。")
        return 0

    for rel, src, dst, expected, why in items:
        old = f"  nas={len(dst.read_bytes())}B" if dst.is_file() else ""
        print(f"[{why:7}] {rel}  local={len(expected)}B{old}")
    print(f"\n合计 {len(items)} 个文件待同步（本地 {len(local_files())} 个纳入比对）")

    if not args.apply:
        print("\nDRY-RUN：未写入任何文件。加 --apply 执行同步。")
        return 0

    print("\n开始同步 ...")
    ok = 0
    for rel, src, dst, expected, why in items:
        try:
            dst.parent.mkdir(parents=True, exist_ok=True)
            tmp = dst.with_name(dst.name + ".mcsync.tmp")
            tmp.write_bytes(expected)
            os.replace(tmp, dst)
            got = dst.read_bytes()
            if digest(got) != digest(expected):
                print(f"[FAIL ] {rel}  SHA256 不一致")
                return 1
            ok += 1
            print(f"[OK   ] {rel}  {digest(expected)[:12]}")
        except Exception as e:  # noqa: BLE001
            print(f"[FAIL ] {rel}  {type(e).__name__}: {e}")
            return 1
    print(f"\n同步完成：{ok}/{len(items)} 个文件 SHA256 校验一致 -> {nas}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
