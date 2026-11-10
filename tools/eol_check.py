import sys
from pathlib import Path

# 按脚本自身位置定位仓库根，别写死某台机器的绝对路径
# （曾因写死 ROOT 导致换机器后直接 FileNotFoundError，检查形同虚设）
ROOT = Path(__file__).resolve().parent.parent
targets = ["deploy/synology-deploy.sh", "deploy/entrypoint.sh", "Dockerfile", ".dockerignore", "docker-compose.yml"]

out = []
for rel in targets:
    p = ROOT / rel
    if not p.exists():
        out.append(f"MISS {rel}")
        continue
    b = p.read_bytes()
    crlf = b.count(b"\r\n")
    lf = b.count(b"\n")
    out.append(f"{rel}: {len(b)}B  lines={lf}  crlf={crlf}  {'CRLF!!' if crlf else 'LF ok'}")
Path(ROOT / ".tools" / "eol.txt").write_text("\n".join(out), encoding="utf-8")
print("\n".join(out))
