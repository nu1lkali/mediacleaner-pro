#!/usr/bin/env python
"""只读诊断「同组保留条件」在真实库上的效果，解释一键应用返回的三个数字。

用法：
    python tools/keepercheck.py <db路径> [kind]

例：
    python tools/keepercheck.py //192.168.2.124/docker/mediacleaner-data/data/mediacleaner.db exact
    python tools/keepercheck.py /volume1/docker/mediacleaner-data/data/mediacleaner.db

全程以 mode=ro 打开，不会改动生产库。判定口径与 backend/internal/keeper/rule.go
的 Resolve() 保持一致，可用来核对「N 组保留文件发生变化」到底由哪些情况构成。
"""
from __future__ import annotations

import shutil
import sqlite3
import sys
import tempfile
from collections import Counter
from pathlib import Path


def open_readonly(db: Path) -> sqlite3.Connection:
    """UNC/SMB 路径无法直接用 SQLite 的 file: URI 打开（会把主机段当 authority），
    因此先把 db + wal + shm 复制成本地只读快照，避免触碰正在运行的生产库。"""
    s = str(db)
    target = db
    if s.startswith("//") or s.startswith("\\\\"):
        tmp = Path(tempfile.mkdtemp(prefix="keepercheck-"))
        for suffix in ("", "-wal", "-shm"):
            src = Path(s + suffix)
            if src.exists():
                shutil.copy2(src, tmp / (db.name + suffix))
        target = tmp / db.name
        print(f"(UNC 路径：已复制只读快照到 {tmp})")
    con = sqlite3.connect(str(target))
    con.execute("PRAGMA query_only=1")
    return con


def depth(path: str) -> int:
    return path.replace("\\", "/").count("/")


def pick_shallowest(members: list[tuple]) -> int:
    """members: (file_id, path, mtime_ns, btime_ns, active, is_keeper)
    与 Resolve(..., shallowest_path)：depth 升序 → path 字典序 → file_id 升序。
    """
    active = [m for m in members if m[4]]
    if not active:
        return 0
    active.sort(key=lambda m: (depth(m[1]), m[1], m[0]))
    return active[0][0]


def main() -> int:
    if len(sys.argv) < 2:
        print("用法: python tools/keepercheck.py <db路径> [kind]")
        return 2
    db = Path(sys.argv[1])
    kind = sys.argv[2] if len(sys.argv) > 2 else "exact"
    if not db.exists():
        print(f"数据库不存在: {db}")
        return 2

    con = open_readonly(db)
    con.row_factory = sqlite3.Row

    print(f"db   = {db}")
    print(f"kind = {kind}")

    print("\n== 分组类型 / 条件配置 ==")
    for r in con.execute(
        "SELECT kind, COUNT(*) groups, SUM(CASE WHEN keeper_rule<>'' THEN 1 ELSE 0 END) configured "
        "FROM dup_groups GROUP BY kind ORDER BY groups DESC"
    ):
        print(f"  {r['kind']:<8} 组数={r['groups']:<8} 已配置条件={r['configured']}")
    rules = Counter(
        r[0] for r in con.execute("SELECT COALESCE(keeper_rule,'(空)') FROM dup_groups WHERE kind=?", (kind,))
    )
    print(f"  {kind} 的条件分布: {dict(rules)}")

    print("\n== 每组 keeper 数量分布（应全部为 1）==")
    counts = Counter()
    for r in con.execute(
        "SELECT g.id, SUM(m.is_keeper) k FROM dup_groups g "
        "JOIN dup_group_members m ON m.group_id=g.id WHERE g.kind=? GROUP BY g.id", (kind,)
    ):
        counts[r["k"]] += 1
    print(f"  {dict(counts)}   → 合计 keeper 文件数 = {sum(k * n for k, n in counts.items())}")

    print("\n== keep_reason 分布（保留判定的依据）==")
    for reason, n in con.execute(
        "SELECT COALESCE(m.keep_reason,'(空)') reason, COUNT(*) n "
        "FROM dup_group_members m JOIN dup_groups g ON g.id=m.group_id "
        "WHERE g.kind=? AND m.is_keeper=1 GROUP BY reason ORDER BY n DESC LIMIT 20", (kind,)
    ):
        print(f"  {n:>7}  {reason}")

    print("\n== 若按「路径最浅」重算：变化组数及原因 ==")
    rows = con.execute(
        "SELECT g.id, m.file_id, f.path, f.mtime_ns, "
        "       COALESCE(f.btime_ns,0), f.state, m.is_keeper "
        "FROM dup_groups g JOIN dup_group_members m ON m.group_id=g.id "
        "JOIN files f ON f.id=m.file_id WHERE g.kind=? ORDER BY g.id, m.file_id", (kind,)
    )
    groups: dict[int, list[tuple]] = {}
    for r in rows:
        groups.setdefault(r[0], []).append(
            (r[1], r[2], r[3], r[4], r[5] == "active", r[6] == 1)
        )

    reasons = Counter()
    samples: list[str] = []
    for gid, members in groups.items():
        before = [m[0] for m in members if m[5]]
        old = before[0] if before else 0
        active = [m for m in members if m[4]]
        new = pick_shallowest(members)
        if new == 0 or old == new:
            reasons["未变化"] += 1
            continue
        if old == 0:
            why = "原来没有 keeper"
        elif old not in [m[0] for m in active]:
            why = "原 keeper 已不在 active（历史清理/失效）"
        elif len(active) == 1:
            why = "组内仅剩 1 个有效副本，强制保留"
        else:
            why = "原 keeper 不符合条件，按条件换人"
        reasons[why] += 1
        if len(samples) < 5:
            old_path = next((m[1] for m in members if m[0] == old), "-")
            new_path = next((m[1] for m in members if m[0] == new), "-")
            samples.append(f"  #{gid}: {old_path}  →  {new_path}   [{why}]")

    for why, n in reasons.most_common():
        print(f"  {n:>7}  {why}")
    print(f"  → 实际会变化的组数 = {sum(n for k, n in reasons.items() if k != '未变化')}")
    if samples:
        print("\n  变化样例：")
        for s in samples:
            print(s)

    print("\n== 创建时间（btime）可用率 ==")
    r = con.execute(
        "SELECT COALESCE(SUM(CASE WHEN f.btime_ns>0 THEN 1 ELSE 0 END),0), COUNT(*) "
        "FROM dup_group_members m JOIN dup_groups g ON g.id=m.group_id "
        "JOIN files f ON f.id=m.file_id WHERE g.kind=? AND f.state='active'", (kind,)
    ).fetchone()
    total = r[1] or 1
    print(f"  已知 {r[0]} / {r[1]}  ({r[0] * 100 / total:.1f}%)")

    print("\n== 可删副本数（换 keeper 不改变这个数）==")
    r = con.execute(
        "SELECT COUNT(*) FROM dup_group_members m JOIN dup_groups g ON g.id=m.group_id "
        "JOIN files f ON f.id=m.file_id WHERE g.kind=? AND m.is_keeper=0 AND f.state='active'", (kind,)
    ).fetchone()
    print(f"  {r[0]}")

    con.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
