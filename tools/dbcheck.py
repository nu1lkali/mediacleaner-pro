#!/usr/bin/env python
"""直接读 SQLite，检查索引结果与指纹字段（排查相似检测为什么没出分组）。

用法: python tools/dbcheck.py [db路径]
默认读 .tools/diag/diag.db，输出 .tools/db.txt
"""
import sqlite3
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DB = Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT / ".tools" / "diag" / "diag.db"
OUT = ROOT / ".tools" / "db.txt"

out = []
p = out.append
p(f"db={DB} exists={DB.exists()}")
if DB.exists():
    con = sqlite3.connect(f"file:{DB}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row

    p("\n== files ==")
    for r in con.execute("""SELECT id, name, ext, media_kind, size, state, hash_status,
                                   COALESCE(phash,'') phash, COALESCE(quick_hash,'') quick,
                                   COALESCE(content_hash,'') content, COALESCE(phash_frames,'') frames
                            FROM files ORDER BY id"""):
        p(f"  #{r['id']:<3} {r['name']:<18} {r['media_kind']:<8} {r['size']:>8} "
          f"state={r['state']:<7} hash={r['hash_status']:<9} "
          f"phash={r['phash'] or '-'} quick={(r['quick'] or '-')[:10]} "
          f"content={(r['content'] or '-')[:10]} frames={(r['frames'] or '-')[:24]}")

    p("\n== file_media ==")
    try:
        for r in con.execute("""SELECT file_id, width, height, duration_ms, bitrate,
                                       COALESCE(video_codec,'') vc, COALESCE(audio_codec,'') ac
                                FROM file_media ORDER BY file_id"""):
            p(f"  #{r['file_id']:<3} {r['width']}x{r['height']} dur={r['duration_ms']}ms "
              f"br={r['bitrate']} v={r['vc']} a={r['ac']}")
    except sqlite3.Error as e:
        p(f"  (读取失败: {e})")

    p("\n== dup_groups ==")
    for r in con.execute("""SELECT id, kind, COALESCE(signature,'') sig, member_count,
                                   total_bytes, reclaimable_bytes, avg_similarity
                            FROM dup_groups ORDER BY id"""):
        p(f"  #{r['id']} kind={r['kind']:<7} n={r['member_count']} "
          f"bytes={r['total_bytes']} reclaim={r['reclaimable_bytes']} "
          f"sim={r['avg_similarity']} sig={(r['sig'] or '')[:20]}")

    p("\n== dup_group_members ==")
    for r in con.execute("""SELECT m.group_id, m.file_id, f.name, m.similarity, m.is_keeper,
                                   COALESCE(m.keep_reason,'') reason
                            FROM dup_group_members m JOIN files f ON f.id=m.file_id
                            ORDER BY m.group_id, m.file_id"""):
        p(f"  g{r['group_id']} <- #{r['file_id']} {r['name']:<18} "
          f"sim={r['similarity']} keeper={r['is_keeper']} {r['reason']}")

    p("\n== scan_tasks ==")
    for r in con.execute("""SELECT id, name, status, phase, files_seen, files_indexed,
                                   bytes_scanned, error_count, COALESCE(message,'') msg
                            FROM scan_tasks ORDER BY id"""):
        p(f"  #{r['id']} {r['status']:<10} {r['phase']:<12} seen={r['files_seen']} "
          f"idx={r['files_indexed']} errs={r['error_count']} {r['msg']}")

    p("\n== scan_errors ==")
    for r in con.execute("""SELECT task_id, COALESCE(path,'') path, COALESCE(code,'') code,
                                   COALESCE(message,'') msg FROM scan_errors ORDER BY id LIMIT 60"""):
        p(f"  t{r['task_id']} [{r['code']}] {r['msg']}  <- {r['path']}")

    con.close()

OUT.write_text("\n".join(out) + "\n", encoding="utf-8")
print("\n".join(out))
print(f"\n[wrote {OUT}]")
