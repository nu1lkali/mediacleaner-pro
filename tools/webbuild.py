#!/usr/bin/env python
"""前端构建驱动：tsc 类型检查 + vite 打包，输出统一写入 .tools/web.log。

用法: python tools/webbuild.py [check|build]
"""
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FRONT = ROOT / "frontend"
LOG = ROOT / ".tools" / "web.log"
NODE = Path(r"C:\Users\Administrator\.workbuddy\binaries\node\versions\22.22.2-3\node.exe")

TSC = FRONT / "node_modules" / "typescript" / "bin" / "tsc"
VITE = FRONT / "node_modules" / "vite" / "bin" / "vite.js"


def run(label: str, args: list[str], out: list[str]) -> int:
    out.append(f"$ node {label}")
    env = dict(os.environ)
    env["NODE_ENV"] = "production"
    p = subprocess.run([str(NODE), *args], cwd=str(FRONT), env=env,
                       capture_output=True, text=True, encoding="utf-8", errors="replace")
    txt = ((p.stdout or "") + (p.stderr or "")).strip()
    if txt:
        out.append(txt)
    out.append(f"EXIT={p.returncode}\n")
    return p.returncode


def main() -> int:
    mode = sys.argv[1] if len(sys.argv) > 1 else "build"
    out: list[str] = []
    out.append(f"node: {NODE} exists={NODE.exists()}")
    out.append(f"tsc:  {TSC} exists={TSC.exists()}")
    out.append(f"vite: {VITE} exists={VITE.exists()}\n")
    if not NODE.exists():
        LOG.write_text("\n".join(out), encoding="utf-8")
        return 2

    rc = run("tsc -p tsconfig.json --noEmit", [str(TSC), "-p", "tsconfig.json", "--noEmit", "--pretty", "false"], out)
    if rc != 0:
        out.append("== 类型检查失败，跳过打包 ==")
        LOG.write_text("\n".join(out), encoding="utf-8")
        return rc

    if mode == "check":
        out.append("== 类型检查通过 ==")
        LOG.write_text("\n".join(out), encoding="utf-8")
        return 0

    rc = run("vite build", [str(VITE), "build"], out)
    dist = FRONT / "dist" / "index.html"
    out.append(f"dist/index.html exists={dist.exists()}")
    if dist.exists():
        total = sum(f.stat().st_size for f in (FRONT / "dist").rglob("*") if f.is_file())
        out.append(f"dist 大小={total // 1024}KB")
    LOG.write_text("\n".join(out), encoding="utf-8")
    return rc


if __name__ == "__main__":
    sys.exit(main())
