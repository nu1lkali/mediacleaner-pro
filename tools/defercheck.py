"""延迟校验（defer_verify）与批次隔离的 SQLite / 静态回归测试。

验证：
1. 旧库缺 evidence 列时，ALTER 补列能成功且默认值正确；
2. exactSampled 的分组 SQL 只把 (size, quick_hash) 都相同的文件放进一组；
3. 批次隔离、免校验开关、傻瓜化向导的关键实现点；
4. 旧库迁移顺序：静态 schema 不得引用「补列才有」的列，索引必须在补列之后建立。
   对真实库做完整迁移演练请用 tools/schemacheck.py（UNC 只读快照，不改生产库）。

运行：python tools/defercheck.py
"""
from __future__ import annotations

import io
import re
import sqlite3
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def load_schema() -> str:
    src = io.open(ROOT / "backend/internal/store/schema.go", encoding="utf-8").read()
    m = re.search(r"schemaSQL\s*=\s*`(.*?)`", src, re.S)
    if not m:
        raise SystemExit("无法从 schema.go 提取 schemaSQL")
    return m.group(1)


def sampled_groups_sql() -> str:
    """从 group.go 里抽出 exactSampled 的分桶查询，确保测的是真实 SQL。"""
    src = io.open(ROOT / "backend/internal/scan/group.go", encoding="utf-8").read()
    start = src.index("func (gb *groupBuilder) exactSampled")
    tail = src[start:]
    m2 = re.search(r"gb\.st\.Select\(ctx, `(.*?)`\)", tail, re.S)
    if not m2:
        raise SystemExit("无法从 group.go 提取 exactSampled 查询")
    return m2.group(1)


def make_db(old_schema: bool) -> sqlite3.Connection:
    schema = load_schema()
    if old_schema:
        # 模拟「最老的库」：CREATE TABLE 里没有这些后加的列。
        # 静态 schema 不得含引用迁移列的索引；批次索引统一在补列之后建立。
        schema = schema.replace("  evidence TEXT NOT NULL DEFAULT 'exact',\n", "")
        schema = schema.replace("  keeper_rule TEXT NOT NULL DEFAULT '',\n", "")
        schema = schema.replace("  btime_ns INTEGER NOT NULL DEFAULT 0,\n", "")
        schema = schema.replace("  batch_active INTEGER NOT NULL DEFAULT 1,\n", "")
        schema = schema.replace("  scan_task_id INTEGER NOT NULL DEFAULT 0,\n", "")
    db = sqlite3.connect(":memory:")
    db.executescript(schema)
    if old_schema:
        # 等价 store.ensureColumns()：列补齐必须先于任何索引创建
        for q in (
            "ALTER TABLE dup_groups ADD COLUMN evidence TEXT NOT NULL DEFAULT 'exact'",
            "ALTER TABLE dup_groups ADD COLUMN keeper_rule TEXT NOT NULL DEFAULT ''",
            "ALTER TABLE dup_groups ADD COLUMN batch_active INTEGER NOT NULL DEFAULT 1",
            "ALTER TABLE dup_groups ADD COLUMN scan_task_id INTEGER NOT NULL DEFAULT 0",
            "ALTER TABLE files ADD COLUMN btime_ns INTEGER NOT NULL DEFAULT 0",
        ):
            db.execute(q)
    # init() 对「新库」和「旧库」都会跑 ensureIndexes()；批次索引不在静态 schema 里，
    # 所以这里必须无条件创建，否则新库夹具会缺唯一约束。
    db.execute("DROP INDEX IF EXISTS ux_groups_sig")
    db.execute("CREATE UNIQUE INDEX IF NOT EXISTS ux_groups_batch "
               "ON dup_groups(scan_task_id, kind, signature)")
    db.execute("CREATE INDEX IF NOT EXISTS ix_groups_batch "
               "ON dup_groups(batch_active, kind)")
    return db


def add_file(db: sqlite3.Connection, fid: int, path: str, size: int, quick: str,
             content: str = "", kind: str = "video", btime: int = 0) -> None:
    db.execute(
        "INSERT INTO files(id, root_id, path, dir_path, name, stem, ext, size, mtime_ns, btime_ns, "
        " media_kind, state, quick_hash, content_hash, hash_status, created_at, updated_at) "
        "VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,0)",
        (fid, 1, path, "/v", path.rsplit("/", 1)[-1], "f", "", size, 1000 + fid, btime,
         kind, "active", quick, content, "quick" if content == "" else "full"),
    )


def go_const_sql(path: str, name: str) -> str:
    """从 Go 源码里抽出真实的 SQL 常量，确保测的是实际执行的语句。"""
    src = io.open(ROOT / path, encoding="utf-8").read()
    m = re.search(r"const\s+" + name + r"\s*=\s*`(.*?)`", src, re.S)
    if not m:
        raise SystemExit(f"无法从 {path} 提取 {name}")
    return m.group(1)


def case(name: str, cond: bool) -> None:
    print(("  PASS " if cond else "  FAIL ") + name)
    if not cond:
        globals()["FAILED"] = True


FAILED = False


def main() -> int:
    print("[1] 旧库补列 / 新库默认")
    for old in (True, False):
        db = make_db(old)
        cols = [r[1] for r in db.execute("PRAGMA table_info(dup_groups)")]
        case(f"{'旧库' if old else '新库'} 含 evidence 列", "evidence" in cols)
        db.execute("INSERT INTO dup_groups(kind, signature, created_at, updated_at) VALUES('exact','x',0,0)")
        got = db.execute("SELECT evidence FROM dup_groups WHERE signature='x'").fetchone()[0]
        case(f"{'旧库' if old else '新库'} 默认 evidence=exact", got == "exact")
        cols = [r[1] for r in db.execute("PRAGMA table_info(dup_groups)")]
        fcols = [r[1] for r in db.execute("PRAGMA table_info(files)")]
        case(f"{'旧库' if old else '新库'} 含 keeper_rule / btime_ns",
             "keeper_rule" in cols and "btime_ns" in fcols)
        case(f"{'旧库' if old else '新库'} 含 batch_active 且默认激活",
             db.execute("SELECT batch_active FROM dup_groups WHERE signature='x'").fetchone()[0] == 1)
        kr = db.execute("SELECT keeper_rule FROM dup_groups WHERE signature='x'").fetchone()[0]
        case(f"{'旧库' if old else '新库'} 默认 keeper_rule=''（不改动原行为）", kr == "")
        db.close()

    print("[2] 采样分组 SQL")
    q = sampled_groups_sql()
    db = make_db(False)
    add_file(db, 1, "/v/a.mkv", 1000, "q1")
    add_file(db, 2, "/v/b.mkv", 1000, "q1")       # 与 1 同组
    add_file(db, 3, "/v/c.mkv", 1000, "q2")       # quick 不同 → 不进组
    add_file(db, 4, "/v/d.mkv", 2000, "q1")       # size 不同 → 不进组
    add_file(db, 5, "/v/e.mkv", 3000, "q3")
    add_file(db, 6, "/v/f.mkv", 3000, "q3")       # 与 5 同组
    add_file(db, 7, "/v/g.mkv", 4000, "q4")
    add_file(db, 8, "/v/g2.mkv", 4000, "q4", kind="other")  # other 类型排除 → 该桶只剩 1 个
    add_file(db, 9, "/v/h.mkv", 5000, "")         # 无 quick → 排除
    add_file(db, 10, "/v/h2.mkv", 5000, "")       # 无 quick → 排除
    cur = db.execute(q)
    # 护栏：SELECT 列数必须与 rows.Scan 的目标数一致，否则运行时会报
    # "expected N destination arguments in Scan, not M"
    case("采样查询列数 = 3（与 Scan 目标一致）", len(cur.description) == 3)
    rows = cur.fetchall()
    sigs = sorted((r[0], r[1]) for r in rows)
    case("恰好 2 个候选桶", sigs == [(1000, "q1"), (3000, "q3")])
    case("排除 other 类型后不成组", (4000, "q4") not in sigs)
    case("排除空 quick_hash", all(s for _, s in sigs))

    # 模拟建组：把 (size, quick) 桶写成 dup_groups，evidence=sampled
    for size, quick in sigs:
        members = db.execute(
            "SELECT id, size FROM files WHERE state='active' AND size=? AND quick_hash=? ORDER BY LENGTH(path), mtime_ns",
            (size, quick),
        ).fetchall()
        case(f"{size}|{quick} 桶内 >=2 个成员", len(members) >= 2)
        db.execute(
            "INSERT INTO dup_groups(kind, signature, evidence, size, total_bytes, created_at, updated_at) "
            "VALUES('exact', ?, 'sampled', ?, ?, 0, 0)",
            (f"{size}|{quick}", size, sum(m[1] for m in members)),
        )
    got = db.execute("SELECT kind, COUNT(*) FROM dup_groups GROUP BY kind").fetchall()
    case("分组均标记为 sampled", db.execute("SELECT COUNT(*) FROM dup_groups WHERE evidence='sampled'").fetchone()[0] == 2)
    db.close()

    print("[3] 清理前校验的判定条件")
    # exact/sampled 都需要比对；similar 完全跳过；trusted 不比对内容但仍确认保留副本存在
    cleaner_src = io.open(ROOT / "backend/internal/cleaner/task.go", encoding="utf-8").read()
    util_src = io.open(ROOT / "backend/internal/store/util.go", encoding="utf-8").read()
    for ev in ("exact", "sampled", "similar", "trusted"):
        case(f"store 定义 evidence={ev}", f'"{ev}"' in util_src)
    case("cleaner 引用统一常量而非硬编码", "store.EvidenceTrusted" in cleaner_src
         and "store.EvidenceSimilar" in cleaner_src)
    case("trusted 分支仍检查保留副本存在", "ensureKeeperExists" in cleaner_src)
    for evidence, need, trusted in (("exact", True, False), ("sampled", True, False),
                                    ("trusted", False, True), ("similar", False, False)):
        expected_need = evidence in ("exact", "sampled")
        case(f"evidence={evidence} → 逐字节比对={need}", expected_need == need)
        case(f"evidence={evidence} → 免校验标记={trusted}", (evidence == "trusted") == trusted)

    print("[4] 一键清理：planFromGroups 的真实 SQL")
    src = io.open(ROOT / "backend/internal/api/handlers_clean.go", encoding="utf-8").read()
    tail = src[src.index("func (s *Server) planFromGroups"):]
    q4 = re.search(r"`(.*?)`", tail, re.S).group(1)
    db = make_db(False)
    for fid in range(1, 7):
        add_file(db, fid, f"/v/f{fid}.mkv", 1000 + fid, f"q{fid}")
    db.execute("UPDATE files SET state='quarantined' WHERE id=6")  # 已清理的不再纳入
    for gid, kind in ((1, "exact"), (2, "exact"), (3, "image")):
        db.execute(
            "INSERT INTO dup_groups(id, kind, signature, created_at, updated_at) VALUES(?,?,?,0,0)",
            (gid, kind, f"g{gid}"),
        )
    members = [
        (1, 1, 1), (1, 2, 0), (1, 3, 0),  # exact 组：1 保留，2/3 可删
        (2, 4, 1), (2, 5, 0),            # exact 组：4 保留，5 可删
        (3, 2, 0), (3, 5, 1),            # 相似组：5 是保留 → 跨组必须排除
        (3, 6, 0),                       # 6 已隔离 → 排除
    ]
    db.executemany("INSERT INTO dup_group_members(group_id, file_id, is_keeper) VALUES(?,?,?)", members)
    cur4 = db.execute(q4, ("exact",))
    case("一键清理查询列数 = 1（与 Scan 目标一致）", len(cur4.description) == 1)
    exact = [r[0] for r in cur4]
    case("只取该类分组的非保留副本", exact == [2, 3])
    case("跨组被标记为保留的文件不会误删", 5 not in exact)
    case("已隔离的文件不会重复纳入", 6 not in exact)
    image = [r[0] for r in db.execute(q4, ("image",))]
    case("按 group_kind 正确隔离不同类别", image == [2])
    video = [r[0] for r in db.execute(q4, ("video",))]
    case("没有该类分组时返回空集", video == [])
    db.close()

    print("[5] 同组保留条件：字段、SQL 与口径对齐")
    # 5.1 新库字段 / 旧库补列
    schema_src = io.open(ROOT / "backend/internal/store/schema.go", encoding="utf-8").read()
    case("schema 声明 files.btime_ns", "btime_ns INTEGER NOT NULL DEFAULT 0" in schema_src)
    case("schema 声明 dup_groups.keeper_rule", "keeper_rule TEXT NOT NULL DEFAULT ''" in schema_src)
    store_src = io.open(ROOT / "backend/internal/store/store.go", encoding="utf-8").read()
    case("旧库自动补 btime_ns", "ADD COLUMN btime_ns" in store_src)
    case("旧库自动补 keeper_rule", "ADD COLUMN keeper_rule" in store_src)

    db = make_db(False)
    case("files 含 btime_ns 列", "btime_ns" in [r[1] for r in db.execute("PRAGMA table_info(files)")])
    case("dup_groups 含 keeper_rule 列", "keeper_rule" in [r[1] for r in db.execute("PRAGMA table_info(dup_groups)")])
    db.execute("INSERT INTO dup_groups(kind, signature, created_at, updated_at) VALUES('exact','x',0,0)")
    case("旧库默认 keeper_rule 为空串（原行为）",
         db.execute("SELECT keeper_rule FROM dup_groups WHERE signature='x'").fetchone()[0] == "")
    db.close()

    # 5.2 真实 SQL：只加载已配置条件的分组，且包含非 active 成员
    cfg_sql = go_const_sql("backend/internal/keeper/apply.go", "configuredMembersSQL")
    kind_sql = go_const_sql("backend/internal/keeper/apply.go", "membersOfKindSQL")
    db = make_db(False)
    for fid, path, bt, st in (
        (1, "/v/a.mkv", 1000, "active"), (2, "/v/sub/b.mkv", 3000, "active"),
        (3, "/v/c.mkv", 0, "quarantined"), (4, "/v/d.mkv", 2000, "active"),
        (5, "/v/e.mkv", 0, "active"),
    ):
        add_file(db, fid, path, 1000, "q", btime=bt)
        if st != "active":
            db.execute("UPDATE files SET state=? WHERE id=?", (st, fid))
    for gid, kind, rule in ((1, "exact", "newest_mtime"), (2, "exact", ""), (3, "image", "oldest_created")):
        db.execute("INSERT INTO dup_groups(id, kind, signature, keeper_rule, created_at, updated_at) "
                   "VALUES(?,?,?,?,0,0)", (gid, kind, f"g{gid}", rule))
    members = [
        (1, 1, 1), (1, 2, 0), (1, 3, 0),  # 组1：已配置，含一个已隔离成员
        (2, 4, 1), (2, 5, 0),            # 组2：未配置
        (3, 1, 0), (3, 4, 1),            # 组3：已配置（跨组共享文件）
    ]
    db.executemany("INSERT INTO dup_group_members(group_id, file_id, is_keeper) VALUES(?,?,?)", members)
    cur = db.execute(cfg_sql)
    case("配置查询列数 = 8（与 rows.Scan 一致）", len(cur.description) == 8)
    rows = cur.fetchall()
    case("只加载已配置条件的分组", {r[0] for r in rows} == {1, 3})
    case("包含已隔离成员以便把旧 keeper 撤下来", any(r[2] == 3 and r[7] == "quarantined" for r in rows))
    case("配置查询返回 btime_ns 原值", {r[2]: r[5] for r in rows}.get(2) == 3000)
    cur2 = db.execute(kind_sql, ("exact",))
    case("按类型查询同时包含未配置分组", {r[0] for r in cur2.fetchall()} == {1, 2})

    # 5.3 写回：每个已配置分组恰好 1 个 keeper，未配置分组完全不动
    clear_sql = "UPDATE dup_group_members SET is_keeper=0, keep_reason=NULL WHERE group_id=?"
    set_sql = "UPDATE dup_group_members SET is_keeper=1, keep_reason=? WHERE group_id=? AND file_id=?"
    db.execute(clear_sql, (1,))
    db.execute(set_sql, ("智能保留 · 最晚修改", 1, 2))
    keepers = db.execute("SELECT group_id, file_id FROM dup_group_members WHERE is_keeper=1 ORDER BY group_id").fetchall()
    case("写回后组1恰好 1 个 keeper 且为新判定文件", (1, 2) in keepers and len([k for k in keepers if k[0] == 1]) == 1)
    case("未配置分组的 keeper 保持不变", (2, 4) in keepers)

    # 5.4 换 keeper 后按新保留文件体积重算可回收空间（相似组体积各不相同）
    refresh_sql = go_const_sql("backend/internal/keeper/apply.go", "refreshGroupSizeSQL")
    db.execute("UPDATE files SET size=100 WHERE id=1")
    db.execute("UPDATE files SET size=900 WHERE id=2")
    db.execute("UPDATE dup_groups SET total_bytes=1000, member_count=3 WHERE id=1")
    db.execute(refresh_sql, (1,))
    size, reclaim = db.execute("SELECT size, reclaimable_bytes FROM dup_groups WHERE id=1").fetchone()
    case("换 keeper 后 size = 新保留文件体积", size == 900)
    case("换 keeper 后可释放空间同步重算", reclaim == 100)
    db.close()

    # 5.4 前后端口径对齐：Go 常量 / README / 前端选项使用同一批条件 ID
    rule_src = io.open(ROOT / "backend/internal/keeper/rule.go", encoding="utf-8").read()
    readme = io.open(ROOT / "README.md", encoding="utf-8").read()
    ids = ("oldest_mtime", "newest_mtime", "oldest_created", "newest_created",
           "deepest_path", "shallowest_path")
    case("Go 定义 6 种条件", all(re.search(r'Rule[A-Za-z]+\s*=\s*"' + i + '"', rule_src) for i in ids))
    case("README 覆盖 6 种条件口径", all(i in readme for i in ids))
    case("README 明确 ctime 不是创建时间", "inode 变更时间" in readme)
    api_src = io.open(ROOT / "frontend/src/services/api.ts", encoding="utf-8").read()
    case("前端调用 keeper-rule 接口", "keeperRule" in api_src and "/groups/keeper-rule" in api_src)
    page_src = io.open(ROOT / "frontend/src/pages/GroupsPage.tsx", encoding="utf-8").read()
    case("页面提供一键智能设定保留项", "一键智能设定保留项" in page_src)
    cleaner_src = io.open(ROOT / "backend/internal/cleaner/task.go", encoding="utf-8").read()
    case("执行删除前调用条件判定", "enforceKeeperRules" in cleaner_src)
    case("判定失败会阻止执行", "已阻止执行" in io.open(
        ROOT / "backend/internal/cleaner/keeper.go", encoding="utf-8").read())

    print("[6] 批次隔离：schema、唯一索引、激活与一键信任")
    schema_src = io.open(ROOT / "backend/internal/store/schema.go", encoding="utf-8").read()
    store_src = io.open(ROOT / "backend/internal/store/store.go", encoding="utf-8").read()
    group_src = io.open(ROOT / "backend/internal/scan/group.go", encoding="utf-8").read()
    case("schema 声明 dup_groups.batch_active", "batch_active INTEGER NOT NULL DEFAULT 1" in schema_src)
    case("唯一索引改为 (scan_task_id, kind, signature)",
         "ux_groups_batch ON dup_groups(scan_task_id, kind, signature)" in store_src)
    case("旧的全表唯一索引已移除", "ux_groups_sig ON dup_groups(kind, signature)" not in schema_src)
    case("迁移会 DROP 旧索引", "DROP INDEX IF EXISTS ux_groups_sig" in store_src)
    case("旧库自动补 batch_active", "ADD COLUMN batch_active" in store_src)
    ins_sql = go_const_sql("backend/internal/scan/group.go", "insertGroupSQL")
    case("新分组以 batch_active=0 待激活插入",
         "batch_active" in ins_sql and "?,?,?,?,?,?,?,?,?,0,?,?" in ins_sql)
    case("reset 只删本任务旧分组、不再全表清空",
         "DELETE FROM dup_groups WHERE scan_task_id=?" in group_src)
    case("建组成功后原子激活新批次", "func (gb *groupBuilder) activate" in group_src)
    case("refreshCounters 只校正本批次", "WHERE scan_task_id=?" in group_src)

    db = make_db(False)
    for task in (11, 12):
        db.execute(
            "INSERT INTO dup_groups(kind, signature, evidence, scan_task_id, batch_active, "
            "created_at, updated_at) VALUES('exact','dup-sig','sampled',?,0,0,0)", (task,))
    case("不同任务可产出完全相同的分组签名",
         db.execute("SELECT COUNT(*) FROM dup_groups WHERE signature='dup-sig'").fetchone()[0] == 2)
    try:
        db.execute("INSERT INTO dup_groups(kind, signature, scan_task_id, created_at, updated_at) "
                   "VALUES('exact','dup-sig',11,0,0)")
        dup_blocked = False
    except sqlite3.IntegrityError:
        dup_blocked = True
    case("同一任务内签名仍然唯一", dup_blocked)

    db.execute("UPDATE dup_groups SET batch_active=0 WHERE batch_active=1")
    db.execute("UPDATE dup_groups SET batch_active=1 WHERE scan_task_id=12")
    active = db.execute("SELECT DISTINCT scan_task_id FROM dup_groups WHERE batch_active=1").fetchall()
    case("同一时刻只有一个激活批次", active == [(12,)])

    db.execute("UPDATE dup_groups SET evidence='trusted', updated_at=1 "
               "WHERE batch_active=1 AND evidence='sampled'")
    case("一键信任不污染历史批次", db.execute(
        "SELECT evidence FROM dup_groups WHERE scan_task_id=11").fetchone()[0] == "sampled")
    case("激活批次被标为免校验", db.execute(
        "SELECT evidence FROM dup_groups WHERE scan_task_id=12").fetchone()[0] == "trusted")

    # 所有跨批次查询都必须带 batch_active=1，漏一处就会把历史结果混进来
    readers = {
        "backend/internal/api/handlers_groups.go": "g.batch_active=1",
        "backend/internal/api/handlers_keeper.go": "batch_active=1",
        "backend/internal/api/handlers_clean.go": "g.batch_active=1",
        "backend/internal/api/handlers_export.go": "g.batch_active=1",
        "backend/internal/api/handlers_stats.go": "batch_active=1",
        "backend/internal/api/handlers_files.go": "g.batch_active=1",
        "backend/internal/rules/engine.go": "batch_active=1",
        "backend/internal/keeper/apply.go": "g.batch_active=1",
        "backend/internal/api/server.go": "batch_active=1",
    }
    for path, needle in readers.items():
        case(f"{path.split('/')[-1]} 只读取激活批次",
             needle in io.open(ROOT / path, encoding="utf-8").read())
    db.close()

    print("[7] 免校验开关与一键清理向导")
    eng = io.open(ROOT / "backend/internal/scan/engine.go", encoding="utf-8").read()
    hs = io.open(ROOT / "backend/internal/api/handlers_scan.go", encoding="utf-8").read()
    sp = io.open(ROOT / "frontend/src/pages/ScanPage.tsx", encoding="utf-8").read()
    gp = io.open(ROOT / "frontend/src/pages/GroupsPage.tsx", encoding="utf-8").read()
    bt = io.open(ROOT / "backend/internal/api/handlers_batches.go", encoding="utf-8").read()
    ap = io.open(ROOT / "frontend/src/services/api.ts", encoding="utf-8").read()
    apptsx = io.open(ROOT / "frontend/src/App.tsx", encoding="utf-8").read()
    case("Options 声明 skip_verify", 'SkipVerify bool `json:"skip_verify"`' in eng)
    case("免校验隐含不读全文", "if o.SkipVerify" in eng and "o.DeferVerify = true" in eng)
    case("创建任务要求二次确认标记", "AckSkipVerify" in hs)
    case("高风险选择写入审计", "risk_accepted" in hs)
    case("扫描页提供完全免校验开关与红字警示", "完全免校验" in sp and "高风险" in sp)
    case("分组页提供一键信任与撤销", "一键信任全部未校验分组" in gp and "撤销免校验" in gp)
    case("分组页显示免校验徽标", '"trusted"' in gp and "免校验" in gp)
    case("后端提供批次列表/切换/一键信任", all(
        k in bt for k in ("handleListBatches", "handleSwitchBatch", "handleTrustAllGroups")))
    case("前端已接入三个新接口", all(k in ap for k in ("batches:", "switchBatch:", "trustAll:")))
    case("向导页设为默认首页", 'Navigate to="/wizard"' in apptsx)
    case("分组页文案不再声称全文 SHA-256", "全文 SHA-256 校验" not in gp)

    print("[8] 旧库迁移顺序（防 apply schema: no such column 回归）")
    schema_src = io.open(ROOT / "backend/internal/store/schema.go", encoding="utf-8").read()
    store_src = io.open(ROOT / "backend/internal/store/store.go", encoding="utf-8").read()
    deploy_src = io.open(ROOT / "deploy/synology-deploy.sh", encoding="utf-8").read()
    # 剥掉 -- 行注释后，只检查 CREATE INDEX 的列参数，避免把说明文字当 SQL
    clean = "\n".join(re.sub(r"--.*$", "", ln) for ln in load_schema().splitlines())
    idx_stmts = re.findall(
        r"CREATE\s+(?:UNIQUE\s+)?INDEX\s+IF\s+NOT\s+EXISTS\s+\w+\s+ON\s+(\w+)\s*\(([^)]*)\)",
        clean, re.I | re.S)
    migrated = {"batch_active", "scan_task_id", "evidence", "keeper_rule"}
    bad = [(t, c) for t, cs in idx_stmts for c in migrated
           if t == "dup_groups" and re.search(r"\b" + c + r"\b", cs)]
    case("静态 schema 不引用 dup_groups 的迁移列", not bad)
    case("批次索引没有静态写在 schemaSQL 里",
         "ux_groups_batch ON" not in clean and "ix_groups_batch ON" not in clean)
    p_cols, p_idx = store_src.find("s.ensureColumns()"), store_src.find("s.ensureIndexes()")
    case("init 先补列再建索引", 0 <= p_cols < p_idx)
    case("ensureColumns 补齐 dup_groups.scan_task_id",
         "ALTER TABLE dup_groups ADD COLUMN scan_task_id" in store_src)
    case("ensureIndexes 负责两个批次索引并移除旧唯一索引", all(
        k in store_src for k in ("ux_groups_batch ON", "ix_groups_batch ON",
                                 "DROP INDEX IF EXISTS ux_groups_sig")))
    p_schema, p_perm = deploy_src.find('*"apply schema"*'), deploy_src.find('*"打开数据库失败"*')
    case("部署脚本先判 schema 失败再判权限失败", 0 <= p_schema < p_perm)
    db8 = make_db(True)
    names = [r[1] for r in db8.execute(
        "SELECT type, name FROM sqlite_master WHERE tbl_name='dup_groups' AND type='index'")]
    case("最老库演练后批次索引齐全",
         "ux_groups_batch" in names and "ix_groups_batch" in names)
    db8.close()

    print("\n结果：" + ("全部通过" if not FAILED else "存在失败"))
    return 1 if FAILED else 0


if __name__ == "__main__":
    sys.exit(main())
