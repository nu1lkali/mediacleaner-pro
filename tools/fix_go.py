#!/usr/bin/env python
"""补全 Go 工具链解压：只解压缺失或大小不符的文件，可反复执行（断点续传）。

只读文件会先尝试去掉只读属性再写入。用法：
    python tools/fix_go.py            # 补全
    python tools/fix_go.py go/src     # 只处理指定前缀
"""
import os
import stat
import sys
import time
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ZIP = ROOT / ".tools" / "go.zip"
DEST = ROOT / ".tools"
LOG = ROOT / ".tools" / "extract.log"


def log(msg: str) -> None:
    with LOG.open("a", encoding="utf-8") as f:
        f.write(msg + "\n")


def write_entry(zf: zipfile.ZipFile, info: zipfile.ZipInfo, target: Path) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    try:
        dst = open(target, "wb")
    except PermissionError:
        os.chmod(target, stat.S_IWRITE | stat.S_IREAD)
        dst = open(target, "wb")
    with zf.open(info) as src, dst:
        while True:
            chunk = src.read(1 << 20)
            if not chunk:
                break
            dst.write(chunk)


def main() -> int:
    if not ZIP.exists():
        log(f"缺少安装包 {ZIP}")
        return 2
    prefix = sys.argv[1] if len(sys.argv) > 1 else ""
    start = time.time()
    written = skipped = errors = 0
    with zipfile.ZipFile(ZIP) as zf:
        infos = [i for i in zf.infolist() if i.filename.startswith(prefix)]
        log(f"待检查 {len(infos)} 项（前缀 {prefix or '*'}）")
        for i, info in enumerate(infos):
            target = DEST / info.filename
            try:
                if info.is_dir():
                    target.mkdir(parents=True, exist_ok=True)
                    continue
                if target.exists() and target.stat().st_size == info.file_size:
                    skipped += 1
                else:
                    write_entry(zf, info, target)
                    written += 1
            except Exception as exc:  # noqa: BLE001
                errors += 1
                log(f"FAIL {info.filename}: {exc}")
            if (i + 1) % 1000 == 0:
                log(f"进度 {i+1}/{len(infos)} 写入={written} 跳过={skipped} 失败={errors}")
    log(f"完成：写入 {written}，跳过 {skipped}，失败 {errors}，用时 {time.time()-start:.1f}s")
    return 0 if errors == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
