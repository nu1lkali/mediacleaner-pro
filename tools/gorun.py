#!/usr/bin/env python
"""本地 Go 构建辅助脚本：固定工具链路径与环境变量，输出可靠。

用法：
    python tools/gorun.py build ./...        # 编译全部包
    python tools/gorun.py vet ./...          # 静态检查
    python tools/gorun.py test ./...         # 运行测试
    python tools/gorun.py tidy               # 依赖整理
    python tools/gorun.py linux              # 交叉编译 linux/amd64（验证容器可构建）
    python tools/gorun.py linux arm64        # 交叉编译 linux/arm64（ARM 群晖）
"""
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
GO = ROOT / ".tools" / "go" / "bin" / "go.exe"
if not GO.exists():
    GO = ROOT / ".tools" / "go" / "bin" / "go"
BACKEND = ROOT / "backend"


def main() -> int:
    argv = sys.argv[1:]
    extra_env: dict[str, str] = {}
    if argv and argv[0] == "linux":
        arch = argv[1] if len(argv) > 1 else "amd64"
        out = ROOT / "dist" / f"mediacleaner-linux-{arch}"
        out.parent.mkdir(parents=True, exist_ok=True)
        extra_env = {"GOOS": "linux", "GOARCH": arch}
        argv = ["build", "-trimpath", "-ldflags", "-s -w", "-o", str(out), "./cmd/server"]

    args = argv or ["build", "./..."]
    env = dict(os.environ)
    env["GOROOT"] = str(ROOT / ".tools" / "go")
    env["GOPROXY"] = "https://goproxy.cn"
    env["GOSUMDB"] = "off"
    env["GOFLAGS"] = "-mod=mod"
    env["GOTOOLCHAIN"] = "local"
    env["GOMODCACHE"] = str(ROOT / ".tools" / "gomodcache")
    env["GOCACHE"] = str(ROOT / ".tools" / "gocache")
    env["CGO_ENABLED"] = "0"
    env["PATH"] = str(ROOT / ".tools" / "go" / "bin") + os.pathsep + env.get("PATH", "")
    env.update(extra_env)

    print(f"$ go {' '.join(args)}  (cwd={BACKEND})", flush=True)
    proc = subprocess.run([str(GO), *args], cwd=str(BACKEND), env=env,
                          capture_output=True, text=True, encoding="utf-8", errors="replace")
    out = (proc.stdout or "") + (proc.stderr or "")
    if out.strip():
        print(out)
    print(f"EXIT={proc.returncode}")
    # 同时落盘，便于在被重定向吞掉输出时排查
    log = ROOT / ".tools" / "gorun.log"
    with log.open("a", encoding="utf-8") as f:
        f.write(f"\n=== go {' '.join(args)} ===\n{out}\nEXIT={proc.returncode}\n")
    return proc.returncode


if __name__ == "__main__":
    sys.exit(main())
