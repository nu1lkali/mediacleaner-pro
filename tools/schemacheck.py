#!/usr/bin/env python
"""验证 store.init() 的「建表 → 补列 → 建索引」顺序在旧库上能成功。

背景：schemaSQL 会先整体执行，而 ensureColumns() 才给旧库补列。若 schemaSQL 里
有引用「迁移列」的 CREATE INDEX，旧库就会在第一步报
  apply schema: SQL logic error: no such column: xxx
（新库因为 CREATE TABLE 已含这些列，反而不报错，很容易漏测）。

用法：
    python tools/schemacheck.py
    python tools/schemacheck.py --db //192.168.2.124/docker/mediacleaner-data/data/mediacleaner.db
    python tools/schemacheck.py --legacy   # 额外复现修复前的失败

检查项：
  [1] 真实库（UNC 只读快照）的 dup_groups/files 列与已有索引
  [2] schemaSQL 里的静态 CREATE INDEX 不得引用仅由 ensureColumns 添加的列
  [3] 在真实库副本上按 init() 顺序跑一遍，必须全部成功且数据不丢
  [4] --legacy 时复现修复前的 no such column 错误，证明根因
"""
from __future__ import annotations

import argparse
import re
import shutil
import sqlite3
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_DB = "//192.168.2.124/docker/mediacleaner-data/data/mediacleaner.db"
STORE = ROOT / "backend" / "internal" / "store"


def go_raw_strings(text: str, fn_name: str) -> list[str]:
    """从某个 Go 函数体里提取反引号字符串。"""
    start = text.find(f"func (s *Store) {fn_name}(")
    if start < 0:
        raise RuntimeError(f"找不到函数 {fn_name}")
    end = text.find("\nfunc ", start + 10)
    body = text[start:end if end > 0 else len(text)]
    return re.findall(r"`([^`]*)`", body)


def load_go() -> dict:
    schema_src = (STORE / "schema.go").read_text(encoding="utf-8")
    store_src = (STORE / "store.go").read_text(encoding="utf-8")
    m = re.search(r"const schemaSQL = `([^`]*)`", schema_src, re.S)
    if not m:
        raise RuntimeError("找不到 schemaSQL")
    return {
        "schema": m.group(1),
        "alters": go_raw_strings(store_src, "ensureColumns"),
        "indexes": go_raw_strings(store_src, "ensureIndexes"),
    }


def snapshot(db: Path, tmp: Path) -> Path:
    """UNC/SMB 路径不能直接给 SQLite 的 file: URI 打开，先复制 db+wal+shm。"""
    s = str(db)
    if not (s.startswith("//") or s.startswith("\\\\")):
        return db
    for suffix in ("", "-wal", "-shm"):
        src = Path(s + suffix)
        if src.exists():
            shutil.copy2(src, tmp / (db.name + suffix))
    print(f"(UNC：已复制只读快照 {db.name}(+wal/shm) 到 {tmp})")
    return tmp / db.name


def cols(con: sqlite3.Connection, table: str) -> list[str]:
    return [r[1] for r in con.execute(f"PRAGMA table_info({table})")]


def indexes(con: sqlite3.Connection, table: str) -> list[str]:
    return [r[1] for r in con.execute(
        "SELECT type, name FROM sqlite_master WHERE tbl_name=? AND type='index' ORDER BY name",
        (table,))]


def run_init(con: sqlite3.Connection, cfg: dict, legacy: bool = False) -> None:
    """按 store.init() 的真实顺序执行一遍。"""
    schema = cfg["schema"]
    if legacy:
        schema += (
            "\nCREATE UNIQUE INDEX IF NOT EXISTS ux_groups_batch "
            "ON dup_groups(scan_task_id, kind, signature);"
            "\nCREATE INDEX IF NOT EXISTS ix_groups_batch ON dup_groups(batch_active, kind);\n"
        )
    con.executescript(schema)                      # 1. apply schema
    for q in cfg["alters"]:                        # 2. ensureColumns
        try:
            con.execute(q)
        except sqlite3.OperationalError as e:
            if "duplicate column" not in str(e):
                raise
    for q in cfg["indexes"]:                       # 3. ensureIndexes
        con.execute(q)
    con.commit()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", default=DEFAULT_DB)
    ap.add_argument("--legacy", action="store_true", help="额外复现修复前失败")
    args = ap.parse_args()

    cfg = load_go()
    db = Path(args.db)
    if not db.is_file():
        print(f"数据库不存在：{db}")
        return 2
    tmp = Path(tempfile.mkdtemp(prefix="schemacheck-"))
    src = snapshot(db, tmp)

    # ---------- [1] 真实库结构 ----------
    con = sqlite3.connect(str(src))
    con.row_factory = sqlite3.Row
    dg_cols = cols(con, "dup_groups")
    print("\n[1] 真实库结构")
    print(f"  dup_groups 列：{', '.join(dg_cols)}")
    print(f"  files 列    ：{', '.join(cols(con, 'files'))}")
    print(f"  dup_groups 索引：{', '.join(indexes(con, 'dup_groups')) or '(无)'}")
    n_before = con.execute("SELECT COUNT(*) FROM dup_groups").fetchone()[0]
    print(f"  dup_groups 行数：{n_before}")
    con.close()

    # ---------- [2] 静态索引引用检查 ----------
    # 迁移列按 (表, 列) 记录：files.scan_task_id 与 dup_groups.scan_task_id 是两回事，
    # 前者本来就建在 CREATE TABLE 里，不能被误判。
    migrated = set()
    for q in cfg["alters"]:
        m = re.search(r"ALTER TABLE (\w+) ADD COLUMN (\w+)", q)
        if m:
            migrated.add((m.group(1), m.group(2)))
    print("\n[2] 静态 schema 索引引用检查")
    print(f"  迁移列：{', '.join(f'{t}.{c}' for t, c in sorted(migrated)) or '(无)'}")
    # 先剥掉 -- 行注释，注释里的中文说明不应被当作 SQL 语句
    clean = "\n".join(re.sub(r"--.*$", "", ln) for ln in cfg["schema"].splitlines())
    bad = []
    for stmt in clean.split(";"):
        m = re.search(
            r"CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+(\w+)\s+ON\s+(\w+)\s*\(([^)]*)\)",
            stmt, re.I | re.S)
        if not m:
            continue
        idx_name, table, colpart = m.group(1), m.group(2), m.group(3)
        for mt, mc in migrated:
            if mt == table and re.search(r"\b" + re.escape(mc) + r"\b", colpart):
                bad.append((f"{mt}.{mc}", f"{idx_name} ON {table}({colpart.strip()})"))
    if bad:
        for col, stmt in bad:
            print(f"  FAIL 静态索引引用迁移列 {col}: {stmt}")
        print("  → 必须把这些 CREATE INDEX 移到 ensureIndexes()")
    else:
        print("  PASS 静态 schema 未引用任何迁移列")
    ok2 = not bad

    # ---------- [4] 复现修复前失败 ----------
    ok4 = True
    if args.legacy:
        print("\n[4] 复现修复前（静态 schema 含新索引）")
        legacy_db = tmp / "legacy.db"
        shutil.copy2(src, legacy_db)
        c4 = sqlite3.connect(str(legacy_db))
        try:
            run_init(c4, cfg, legacy=True)
            print("  FAIL 旧库竟未报错，与线上现象不符")
            ok4 = False
        except sqlite3.OperationalError as e:
            print(f"  PASS 复现：{e}")
        finally:
            c4.close()

    # ---------- [3] 修复后完整 init ----------
    print("\n[3] 在真实库副本上按 init() 顺序执行")
    work = tmp / "work.db"
    shutil.copy2(src, work)
    c3 = sqlite3.connect(str(work))
    try:
        run_init(c3, cfg)
        after = cols(c3, "dup_groups")
        need = {"evidence", "keeper_rule", "batch_active", "scan_task_id"}
        missing = need - set(after)
        idx = indexes(c3, "dup_groups")
        n_after = c3.execute("SELECT COUNT(*) FROM dup_groups").fetchone()[0]
        print(f"  dup_groups 列：{', '.join(after)}")
        print(f"  dup_groups 索引：{', '.join(idx)}")
        print(f"  行数：{n_before} -> {n_after}")
        checks = {
            "init 顺序执行成功": True,
            "四个批次相关列齐全": not missing,
            "ux_groups_batch 存在": "ux_groups_batch" in idx,
            "ix_groups_batch 存在": "ix_groups_batch" in idx,
            "旧唯一索引 ux_groups_sig 已移除": "ux_groups_sig" not in idx,
            "分组数据未丢失": n_after == n_before,
        }
        for k, v in checks.items():
            print(f"  {'PASS' if v else 'FAIL'} {k}")
        ok3 = all(checks.values())
    except Exception as e:  # noqa: BLE001
        print(f"  FAIL init 失败：{type(e).__name__}: {e}")
        ok3 = False
    finally:
        c3.close()

    print("\n结果：" + ("全部通过" if (ok2 and ok3 and ok4) else "存在失败项"))
    return 0 if (ok2 and ok3 and ok4) else 1


if __name__ == "__main__":
    raise SystemExit(main())
