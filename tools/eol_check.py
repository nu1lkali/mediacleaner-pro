import sys
from pathlib import Path

ROOT = Path(r"C:\Users\Administrator\WorkBuddy\2026-11-09-14-51-27\MediaCleaner")
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
