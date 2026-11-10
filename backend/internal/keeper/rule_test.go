package keeper

import "testing"

func mem(id int64, path string, mtime, btime int64) Member {
	return Member{FileID: id, Path: path, MtimeNS: mtime, BtimeNS: btime,
		Depth: DepthOf(path), Active: true}
}

func TestResolveRules(t *testing.T) {
	// a: 最早修改、最早创建、路径最浅
	// b: 最晚修改、最晚创建、路径最深
	// c: 中间值
	base := []Member{
		mem(1, "/v/a.mkv", 100, 1000),
		mem(2, "/v/sub/b.mkv", 300, 3000),
		mem(3, "/v/c.mkv", 200, 2000),
	}
	cases := []struct {
		rule string
		want int64
	}{
		{RuleOldestMtime, 1},
		{RuleNewestMtime, 2},
		{RuleOldestCreated, 1},
		{RuleNewestCreated, 2},
		{RuleDeepestPath, 2},
		{RuleShallowestPath, 1},
	}
	for _, c := range cases {
		got := Resolve(base, c.rule)
		if !got.Applicable || got.KeeperID != c.want {
			t.Errorf("%s: keeper=%d applicable=%v, want %d",
				c.rule, got.KeeperID, got.Applicable, c.want)
		}
		if got.Reason == "" {
			t.Errorf("%s: keep_reason 为空", c.rule)
		}
	}
}

func TestResolveNoneKeepsOriginalBehaviour(t *testing.T) {
	got := Resolve([]Member{mem(1, "/v/a", 1, 1), mem(2, "/v/b", 2, 2)}, RuleNone)
	if got.Applicable || got.KeeperID != 0 {
		t.Fatalf("未配置条件时不应干预 keeper: %+v", got)
	}
}

func TestResolveTieBreakByPath(t *testing.T) {
	// mtime 完全相同 → 路径字典序最小者保留
	ms := []Member{mem(9, "/v/z.mkv", 100, 100), mem(8, "/v/m.mkv", 100, 100)}
	if got := Resolve(ms, RuleOldestMtime); got.KeeperID != 8 {
		t.Fatalf("并列时应按路径字典序取最小，得到 %d", got.KeeperID)
	}
	if got := Resolve(ms, RuleNewestMtime); got.KeeperID != 8 {
		t.Fatalf("最晚修改并列时同样按路径字典序，得到 %d", got.KeeperID)
	}
	// 层级相同 → 仍然按字典序
	deep := []Member{mem(1, "/v/x/z.mkv", 1, 1), mem(2, "/v/x/a.mkv", 1, 1)}
	if got := Resolve(deep, RuleDeepestPath); got.KeeperID != 2 {
		t.Fatalf("层数相同时应按路径字典序，得到 %d", got.KeeperID)
	}
}

func TestResolveBtimeUnknown(t *testing.T) {
	// 部分未知：只在已知 btime 的成员里比较，未知者不会被选为 keeper
	ms := []Member{
		mem(1, "/v/a.mkv", 100, 0),
		mem(2, "/v/b.mkv", 200, 5000),
		mem(3, "/v/c.mkv", 300, 7000),
	}
	if got := Resolve(ms, RuleOldestCreated); got.KeeperID != 2 {
		t.Fatalf("应排除 btime 未知成员，得到 %d", got.KeeperID)
	}
	if got := Resolve(ms, RuleNewestCreated); got.KeeperID != 3 {
		t.Fatalf("最晚创建应取 3，得到 %d", got.KeeperID)
	}

	// 全组未知：条件不适用 → 回退组内原有的 active keeper
	unknown := []Member{
		mem(1, "/v/a.mkv", 100, 0),
		mem(2, "/v/b.mkv", 200, 0),
	}
	unknown[1].IsKeeper = true
	got := Resolve(unknown, RuleOldestCreated)
	if !got.Applicable || !got.Fallback || got.KeeperID != 2 {
		t.Fatalf("全组 btime 未知应回退原有 keeper: %+v", got)
	}

	// 原有的 keeper 也已不在（非 active）→ 回退默认「路径最浅」
	unknown[1].Active = false
	unknown[1].IsKeeper = true
	got = Resolve(unknown, RuleOldestCreated)
	if !got.Fallback || got.KeeperID != 1 {
		t.Fatalf("无效 keeper 应回退路径最浅者: %+v", got)
	}
}

func TestResolveProtectsLastActiveCopy(t *testing.T) {
	ms := []Member{
		mem(1, "/v/deep/a.mkv", 100, 100),
		mem(2, "/v/b.mkv", 200, 200),
	}
	ms[0].Active = false // 唯一 active 的是路径更浅的 2
	got := Resolve(ms, RuleDeepestPath)
	if !got.Applicable || got.KeeperID != 2 {
		t.Fatalf("组内仅剩 1 个有效副本时必须强制保留它: %+v", got)
	}
	allGone := []Member{mem(1, "/v/a.mkv", 1, 1)}
	allGone[0].Active = false
	if got := Resolve(allGone, RuleNewestMtime); got.KeeperID != 0 {
		t.Fatalf("无有效成员时不应产生 keeper: %+v", got)
	}
}

func TestDepthOf(t *testing.T) {
	if DepthOf("/v/a.mkv") != 2 || DepthOf("/v/sub/b.mkv") != 3 {
		t.Fatal("路径层数口径应与 scan.pickKeeper 一致")
	}
	if DepthOf(`D:\media\a.mkv`) != DepthOf("D:/media/a.mkv") {
		t.Fatal("Windows 反斜杠应统一折算为正斜杠层数")
	}
	if DepthOf(`D:\media\a.mkv`) != 2 {
		t.Fatal("绝对路径 D:/media/a.mkv 应为 2 层")
	}
}
