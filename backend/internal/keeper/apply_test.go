package keeper

import (
	"context"
	"path/filepath"
	"testing"

	"mediacleaner/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "keeper.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func addRoot(t *testing.T, ctx context.Context, st *store.Store) {
	t.Helper()
	if _, err := st.Exec(ctx,
		`INSERT INTO roots(id, name, host_path, real_path, volume, writable, enabled, created_at)
		 VALUES(1,'test','/v','/v','v',1,1,0)`); err != nil {
		t.Fatalf("insert root: %v", err)
	}
}

func addMemFile(t *testing.T, ctx context.Context, st *store.Store,
	id int64, path string, size, mtime, btime int64) {
	t.Helper()
	if _, err := st.Exec(ctx, `
		INSERT INTO files(id, root_id, path, dir_path, name, stem, ext, size, mtime_ns, btime_ns,
		                  media_kind, state, created_at, updated_at)
		VALUES(?,1,?,?,?,?,?,?,?,?,'video','active',0,0)`,
		id, path, "/v", path, path, "", size, mtime, btime); err != nil {
		t.Fatalf("insert file %d: %v", id, err)
	}
}

func addMemGroup(t *testing.T, ctx context.Context, st *store.Store,
	id int64, kind, rule string, total int64) {
	t.Helper()
	if _, err := st.Exec(ctx, `
		INSERT INTO dup_groups(id, kind, signature, total_bytes, member_count, keeper_rule, created_at, updated_at)
		VALUES(?,?,?,?,3,?,0,0)`, id, kind, "sig-"+kind, total, rule); err != nil {
		t.Fatalf("insert group %d: %v", id, err)
	}
}

func addMemMember(t *testing.T, ctx context.Context, st *store.Store, gid, fid int64, keeper bool) {
	t.Helper()
	bit := 0
	if keeper {
		bit = 1
	}
	if _, err := st.Exec(ctx,
		`INSERT INTO dup_group_members(group_id, file_id, similarity, is_keeper) VALUES(?,?,1,?)`,
		gid, fid, bit); err != nil {
		t.Fatalf("insert member %d/%d: %v", gid, fid, err)
	}
}

func keeperIDs(t *testing.T, ctx context.Context, st *store.Store, gid int64) []int64 {
	t.Helper()
	rows, err := st.Select(ctx, `SELECT file_id FROM dup_group_members WHERE group_id=? AND is_keeper=1`, gid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func TestApplyAndEnforce(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	addRoot(t, ctx, st)
	// 计划按「1 是 keeper」生成；条件要求「最晚修改」→ 3 才该被保留
	addMemFile(t, ctx, st, 1, "/v/a.mkv", 1000, 100, 5000)
	addMemFile(t, ctx, st, 2, "/v/sub/b.mkv", 1000, 300, 3000)
	addMemFile(t, ctx, st, 3, "/v/c.mkv", 1000, 200, 2000)
	addMemGroup(t, ctx, st, 7, "exact", "", 3000)
	addMemMember(t, ctx, st, 7, 1, true)
	addMemMember(t, ctx, st, 7, 2, false)
	addMemMember(t, ctx, st, 7, 3, false)

	// 一键应用：组内 keeper 应从 1 换到 2（mtime 最大）
	stats, err := Apply(ctx, st, "exact", RuleNewestMtime, 0)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if stats.Groups != 1 || stats.Changed != 1 || stats.Protected != 1 {
		t.Fatalf("统计不正确: %+v", stats)
	}
	if got := keeperIDs(t, ctx, st, 7); len(got) != 1 || got[0] != 2 {
		t.Fatalf("一键应用后 keeper 应为 2，实际 %v", got)
	}

	// 默认条件持久化，扫描重建后可用
	if err := SetDefault(ctx, st, "exact", RuleNewestMtime); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	if EffectiveRule(ctx, st, "exact") != RuleNewestMtime {
		t.Fatal("默认条件没有持久化")
	}

	// 执行前复核：即使计划是旧 keeper 生成的，保护集合也必须给出「新的」keeper
	protected, paths, _, err := Enforce(ctx, st)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if !protected[2] || protected[1] {
		t.Fatalf("保护集合应含 2 不含 1: %v", protected)
	}
	if paths[7] != "/v/sub/b.mkv" {
		t.Fatalf("逐字节比对应参照新 keeper: %v", paths)
	}

	// 换 keeper 后按新体积重算可回收空间
	var size, reclaim int64
	if err := st.Get(ctx, []any{&size, &reclaim},
		`SELECT size, reclaimable_bytes FROM dup_groups WHERE id=7`); err != nil {
		t.Fatal(err)
	}
	if size != 1000 || reclaim != 2000 {
		t.Fatalf("可回收空间应按新 keeper 重算，实际 size=%d reclaim=%d", size, reclaim)
	}
}

func TestEnforceProtectsLastActiveCopy(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	addRoot(t, ctx, st)
	// 模拟「计划生成在前」：其后 keeper 被移到隔离区，只剩一个 active 副本
	addMemFile(t, ctx, st, 1, "/v/a.mkv", 1000, 100, 100)
	addMemFile(t, ctx, st, 2, "/v/b.mkv", 1000, 300, 300)
	addMemGroup(t, ctx, st, 8, "exact", RuleDeepestPath, 2000)
	addMemMember(t, ctx, st, 8, 1, true)
	addMemMember(t, ctx, st, 8, 2, false)
	if _, err := st.Exec(ctx, `UPDATE files SET state='quarantined' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	protected, _, _, err := Enforce(ctx, st)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if !protected[2] || len(protected) != 1 {
		t.Fatalf("必须保护组内最后一份 active 副本: %v", protected)
	}
	if got := keeperIDs(t, ctx, st, 8); len(got) != 1 || got[0] != 2 {
		t.Fatalf("失效的旧 keeper 应被撤销，实际 %v", got)
	}
}

func TestUnconfiguredGroupsUntouched(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	addRoot(t, ctx, st)
	addMemFile(t, ctx, st, 1, "/v/a.mkv", 1000, 100, 100)
	addMemFile(t, ctx, st, 2, "/v/b.mkv", 1000, 300, 300)
	addMemGroup(t, ctx, st, 9, "exact", "", 2000) // 未配置
	addMemMember(t, ctx, st, 9, 1, true)
	addMemMember(t, ctx, st, 9, 2, false)

	protected, _, _, err := Enforce(ctx, st)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if len(protected) != 0 {
		t.Fatalf("未配置条件时执行期不应产生保护集合: %v", protected)
	}
	if got := keeperIDs(t, ctx, st, 9); len(got) != 1 || got[0] != 1 {
		t.Fatalf("未配置条件时原有 keeper 必须原样保留: %v", got)
	}
}
