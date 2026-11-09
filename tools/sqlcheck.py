import re, sqlite3, sys
from pathlib import Path

SRC = str(Path(__file__).resolve().parent.parent / "backend" / "internal" / "scan" / "index.go")
src = open(SRC, encoding="utf-8").read()

m = re.search(r"func buildUpsertSQL\(\) string \{.*?return `(.*)`\n\}", src, re.S)
upsert = m.group(1).replace("` + inv + `", "files.size<>excluded.size OR files.mtime_ns<>excluded.mtime_ns")
m2 = re.search(r"const reconcileSQL = `(.*?)`", src, re.S)
reconcile = m2.group(1)

print("SQLite:", sqlite3.sqlite_version)

db = sqlite3.connect(":memory:")
db.executescript("""
CREATE TABLE files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  root_id INTEGER NOT NULL, path TEXT NOT NULL, dir_path TEXT, name TEXT, stem TEXT, ext TEXT,
  size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, ctime_ns INTEGER NOT NULL DEFAULT 0,
  dev INTEGER NOT NULL DEFAULT 0, inode INTEGER NOT NULL DEFAULT 0,
  is_symlink INTEGER NOT NULL DEFAULT 0, media_kind TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'active', hash_status TEXT NOT NULL DEFAULT 'none',
  quick_hash TEXT, content_hash TEXT, phash TEXT, phash_frames TEXT,
  scan_task_id INTEGER, last_seen_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_files_path ON files(root_id, path);
""")

COLS = ["root_id","path","dir_path","name","stem","ext","size","mtime_ns","ctime_ns",
        "dev","inode","is_symlink","media_kind","state","hash_status","scan_task_id",
        "last_seen_at","created_at","updated_at","quick_hash","content_hash","phash","phash_frames"]

def insert(path, size, mtime, dev, inode, task, hs="none", qh=None, ch=None, ph=None):
    vals = dict(root_id=1, path=path, dir_path="/a", name=path.split("/")[-1], stem="s", ext="mp4",
                size=size, mtime_ns=mtime, ctime_ns=0, dev=dev, inode=inode, is_symlink=0,
                media_kind="video", state="active", hash_status=hs, scan_task_id=task,
                last_seen_at=0, created_at=0, updated_at=0,
                quick_hash=qh, content_hash=ch, phash=ph, phash_frames=None)
    db.execute(f"INSERT INTO files({','.join(COLS)}) VALUES({','.join(['?']*len(COLS))})",
               [vals[c] for c in COLS])

def upsert_params(path, size, mtime, dev, inode, task):
    return [1, path, "/a", path.split("/")[-1], "s", "mp4", size, mtime, 0, dev, inode, 0, "video", task, 0, 0, 0]

ok = True
def check(name, cond, detail):
    global ok
    ok &= bool(cond)
    print(("  ✓ " if cond else "  ✗ ") + name + "  " + str(detail))

# 1. upsert：size/mtime 未变 → 指纹保留
insert("/a/x.mp4", 100, 111, 2049, 700, 1, hs="full", qh="Q1", ch="H1", ph="P1")
db.execute(upsert, upsert_params("/a/x.mp4", 100, 111, 2049, 700, 2))
got = db.execute("SELECT content_hash, hash_status, scan_task_id FROM files WHERE path='/a/x.mp4'").fetchone()
check("同 mtime 保留指纹", got == ("H1", "full", 2), got)

# 2. mtime 变了 → 清空
db.execute(upsert, upsert_params("/a/x.mp4", 100, 222, 2049, 700, 2))
got = db.execute("SELECT content_hash, quick_hash, hash_status FROM files WHERE path='/a/x.mp4'").fetchone()
check("mtime 变化清空指纹", got == (None, None, "none"), got)

# 3. 改名：inode 相同 → 继承
insert("/a/old.mp4", 500, 333, 2049, 888, 1, hs="full", qh="Q3", ch="H3", ph="P3")
insert("/a/new.mp4", 500, 333, 2049, 888, 2, hs="none")
cur = db.execute(reconcile, (99, 2))
got = db.execute("SELECT content_hash, quick_hash, phash, hash_status FROM files WHERE path='/a/new.mp4'").fetchone()
check("改名后继承指纹", got == ("H3", "Q3", "P3", "full"), got)

# 4. mtime 不同 → 不继承
insert("/a/m1.mp4", 600, 444, 2049, 999, 1, hs="full", qh="Q4", ch="H4")
insert("/a/m2.mp4", 600, 445, 2049, 999, 2, hs="none")
db.execute(reconcile, (99, 2))
got = db.execute("SELECT content_hash FROM files WHERE path='/a/m2.mp4'").fetchone()
check("mtime 不同不继承", got == (None,), got)

# 5. dev/inode=0（Windows / 网络盘）→ 不继承
insert("/w/old.mp4", 700, 555, 0, 0, 1, hs="full", qh="Q5", ch="H5")
insert("/w/new.mp4", 700, 555, 0, 0, 2, hs="none")
db.execute(reconcile, (99, 2))
got = db.execute("SELECT content_hash FROM files WHERE path='/w/new.mp4'").fetchone()
check("dev/inode=0 不继承", got == (None,), got)

# 6. 已有指纹的行不被覆盖
insert("/a/has.mp4", 800, 666, 2049, 1234, 2, hs="full", qh="Q6", ch="H6")
db.execute(reconcile, (99, 2))
got = db.execute("SELECT content_hash FROM files WHERE path='/a/has.mp4'").fetchone()
check("已有指纹不被覆盖", got == ("H6",), got)

# 7. 只影响本轮任务的行（scan_task_id 不匹配的不动）
insert("/a/other.mp4", 900, 777, 2049, 5555, 1, hs="none")
db.execute(reconcile, (99, 2))
got = db.execute("SELECT content_hash, hash_status FROM files WHERE path='/a/other.mp4'").fetchone()
check("非本轮任务不参与", got == (None, "none"), got)

print()
print("结果:", "全部通过" if ok else "有失败")
sys.exit(0 if ok else 1)
