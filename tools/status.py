#!/usr/bin/env python
"""项目状态巡检：目录结构、二进制、测试数据、工具链。

用法: python tools/status.py
输出: .tools/status.txt
"""
import os
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / ".tools" / "status.txt"


def size(n: int) -> str:
    for unit in ("B", "KB", "MB", "GB"):
        if n < 1024 or unit == "GB":
            return f"{n:.0f}{unit}" if unit == "B" else f"{n:.1f}{unit}"
        n /= 1024
    return f"{n}B"


def main() -> None:
    out: list[str] = []
    p = out.append

    p("== 关键文件 ==")
    for rel in [
        "backend/go.mod", "backend/go.sum",
        "backend/bin/mediacleaner.exe",
        ".tools/go/bin/go.exe",
        "frontend/package.json", "frontend/node_modules/react-dom/client.js",
        "frontend/dist/index.html",
    ]:
        f = ROOT / rel
        p(f"  {'OK ' if f.exists() else 'MISS'} {rel}"
          + (f"  {size(f.stat().st_size)}" if f.exists() and f.is_file() else ""))

    p("\n== 后端源码 ==")
    b = ROOT / "backend"
    if b.exists():
        n = 0
        for dirpath, _, files in os.walk(b):
            if any(s in dirpath for s in ("\\bin", "/bin", ".tools")):
                continue
            for f in files:
                if f.endswith(".go"):
                    n += 1
                    fp = Path(dirpath) / f
                    p(f"  {size(fp.stat().st_size):>8}  {fp.relative_to(ROOT)}")
        p(f"  共 {n} 个 .go 文件")

    p("\n== 前端源码 ==")
    fs = ROOT / "frontend" / "src"
    if fs.exists():
        for fp in sorted(fs.rglob("*")):
            if fp.is_file():
                p(f"  {size(fp.stat().st_size):>8}  {fp.relative_to(ROOT)}")

    p("\n== 测试数据 ==")
    td = ROOT / "testdata"
    if td.exists():
        total = 0
        for fp in sorted(td.rglob("*")):
            if fp.is_file():
                total += fp.stat().st_size
                p(f"  {size(fp.stat().st_size):>8}  {fp.relative_to(td)}")
        p(f"  合计 {size(total)}")
    else:
        p("  缺失")

    p("\n== 部署资产 ==")
    for pat in ("deploy", "docker*", "*.sh", "*.md", "Makefile"):
        for fp in sorted(ROOT.glob(pat)):
            if fp.is_file():
                p(f"  {fp.relative_to(ROOT)}")
            elif fp.is_dir() and not any(s in fp.name for s in (".tools", "node_modules")):
                for sub in sorted(fp.rglob("*")):
                    if sub.is_file():
                        p(f"  {sub.relative_to(ROOT)}")

    OUT.parent.mkdir(exist_ok=True)
    OUT.write_text("\n".join(out) + "\n", encoding="utf-8")
    print("\n".join(out))
    print(f"\n[wrote {OUT}]")


if __name__ == "__main__":
    main()
