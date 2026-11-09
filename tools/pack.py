#!/usr/bin/env python
"""把项目打成可直接拷到 NAS 的源码包，输出到桌面。

包含：全部源码 + 已构建的前端 dist + 预编译的 linux/amd64 二进制 + 部署脚本。
排除：.tools（Go 工具链）、node_modules、data/cache/config（运行期数据）、
      testdata、backend/bin、Python 原型（backend/app、backend/tests）。

用法：
    python tools/pack.py                 # 输出到桌面
    python tools/pack.py --out D:/x      # 指定输出目录
"""
import argparse
import os
import tarfile
import time
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PKG_NAME = "MediaCleaner"

# 顶层直接收的文件
TOP_FILES = ["README.md", "Dockerfile", ".dockerignore", "docker-compose.yml"]

# 需要递归收的目录（相对项目根）
TREES = [
    ("backend/cmd", None),
    ("backend/internal", None),
    ("frontend/src", None),
    ("frontend/public", None),
    ("deploy", None),
    ("tools", None),
]

# 单文件
SINGLE = [
    "backend/go.mod",
    "backend/go.sum",
    "frontend/index.html",
    "frontend/package.json",
    "frontend/package-lock.json",
    "frontend/tailwind.config.js",
    "frontend/postcss.config.js",
    "frontend/tsconfig.json",
    "frontend/tsconfig.node.json",
    "frontend/vite.config.ts",
]

# 目录级排除（任意层级命中即跳过）
EXCLUDE_DIRS = {"node_modules", ".tools", "data", "cache", "config", "testdata",
                "bin", "app", "tests", "dist", "__pycache__", ".git", ".idea", ".vscode"}
EXCLUDE_EXT = {".log", ".db", ".db-shm", ".db-wal", ".exe", ".zip", ".tar", ".gz", ".pyc"}


def should_skip(p: Path) -> bool:
    if p.name.startswith(".") and p.name not in {".dockerignore"}:
        return True
    if p.suffix.lower() in EXCLUDE_EXT:
        return True
    return False


def collect() -> list[tuple[Path, str]]:
    """返回 [(磁盘路径, 包内相对路径)]。"""
    out: list[tuple[Path, str]] = []
    for name in TOP_FILES:
        p = ROOT / name
        if p.is_file():
            out.append((p, name))
    for name in SINGLE:
        p = ROOT / name
        if p.is_file():
            out.append((p, name))
    for rel, _ in TREES:
        base = ROOT / rel
        if not base.is_dir():
            continue
        for dirpath, dirnames, filenames in os.walk(base):
            d = Path(dirpath)
            dirnames[:] = [x for x in dirnames if x not in EXCLUDE_DIRS and not x.startswith(".")]
            for fn in filenames:
                f = d / fn
                if should_skip(f):
                    continue
                out.append((f, f.relative_to(ROOT).as_posix()))
    # 前端产物（供不装 node 的环境直接跑）
    dist = ROOT / "frontend" / "dist"
    if dist.is_dir():
        for f in dist.rglob("*"):
            if f.is_file():
                out.append((f, f.relative_to(ROOT).as_posix()))
    # 预编译二进制
    for f in sorted((ROOT / "dist").glob("mediacleaner-linux-*")):
        out.append((f, f"bin/{f.name}"))
    return sorted(out, key=lambda t: t[1])


def build_dist_script(dest: Path) -> None:
    """包里补一个构建入口，避免用户记不住路径。"""
    text = """#!/bin/sh
# 在开发机/群晖上构建（需要 Go 1.23+ 与 Node 20+）
# 群晖上通常不用这个脚本，直接 bash deploy/synology-deploy.sh 即可（Docker 内构建）。
set -e
cd "$(dirname "$0")"

echo "==> 前端"
cd frontend && npm ci && npm run build && cd ..

echo "==> 后端"
mkdir -p backend/bin
cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/mediacleaner ./cmd/server && cd ..

echo "==> 完成：backend/bin/mediacleaner"
echo "    本地运行：MC_ADMIN_PASSWORD=改成你的密码 ./backend/bin/mediacleaner"
"""
    (dest).write_text(text, encoding="utf-8", newline="\n")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(Path.home() / "Desktop"))
    args = ap.parse_args()

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    files = collect()
    if not files:
        print("没有可打包文件，检查项目路径")
        return 2

    stamp = time.strftime("%Y%m%d")
    stage = f"{PKG_NAME}-{stamp}-src"
    zip_path = out_dir / f"{stage}.zip"
    tgz_path = out_dir / f"{stage}.tar.gz"

    # ---------- zip ----------
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for disk, rel in files:
            z.write(disk, f"{stage}/{rel}")
        # 构建脚本（临时文件，直接写进包）
        import io
        buf = io.BytesIO()
        tmp = Path(out_dir / ".build.sh.tmp")
        build_dist_script(tmp)
        z.write(tmp, f"{stage}/build.sh")
        tmp.unlink()

    # ---------- tar.gz（保留可执行位，Linux 上用） ----------
    with tarfile.open(tgz_path, "w:gz") as t:
        for disk, rel in files:
            info = t.gettarinfo(str(disk), arcname=f"{stage}/{rel}")
            info.uid = info.gid = 0
            info.uname = info.gname = "root"
            info.mtime = int(time.time())
            with disk.open("rb") as fh:
                t.addfile(info, fh)
        tmp = Path(out_dir / ".build.sh.tmp")
        build_dist_script(tmp)
        info = t.gettarinfo(str(tmp), arcname=f"{stage}/build.sh")
        info.mode = 0o755
        info.uid = info.gid = 0
        info.uname = info.gname = "root"
        with tmp.open("rb") as fh:
            t.addfile(info, fh)
        tmp.unlink()
        # 部署脚本置为可执行
        for script in ("deploy/synology-deploy.sh", "deploy/entrypoint.sh"):
            try:
                m = t.getmember(f"{stage}/{script}")
                m.mode = 0o755
            except KeyError:
                pass

    total = sum(p.stat().st_size for p, _ in files)
    print(f"文件数：{len(files) + 1}")
    print(f"原始体积：{total / 1024 / 1024:.1f} MB")
    for p in (zip_path, tgz_path):
        print(f"已生成：{p}  ({p.stat().st_size / 1024 / 1024:.1f} MB)")

    # ---------- 自检：关键文件必须在包里 ----------
    need = ["Dockerfile", "docker-compose.yml", "README.md",
            "backend/go.mod", "backend/cmd/server/main.go",
            "deploy/synology-deploy.sh", "deploy/entrypoint.sh",
            "frontend/package.json", "frontend/src/main.tsx", "build.sh"]
    with zipfile.ZipFile(zip_path) as z:
        names = set(z.namelist())
        missing = [n for n in need if f"{stage}/{n}" not in names]
        bins = [n for n in names if "/bin/mediacleaner-linux-" in n]
        dist_files = [n for n in names if "/frontend/dist/" in n]
    print(f"预编译二进制：{bins or '无'}")
    print(f"前端产物文件数：{len(dist_files)}")
    if missing:
        print(f"缺失关键文件：{missing}")
        return 1
    print("自检通过：包结构完整")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
