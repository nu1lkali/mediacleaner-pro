package cleaner

import (
	"context"
	"fmt"

	"mediacleaner/internal/keeper"
	"mediacleaner/internal/store"
)

// enforceKeeperRules 实现「执行删除前，先依据条件完成组内保留判定」。
//
// 顺序不可颠倒：先由 keeper.Enforce 按当前成员状态重新判定并写回 is_keeper，
// 再用重算后的结果过滤待删清单。这样即使计划是在应用条件之前生成的、或分组
// 被新一轮扫描重建过，也不会把刚被判为保留的那一份删掉。
//
// 未配置任何条件时 keeper.Enforce 返回空集合，本函数不改动任何待删项，
// 原有去重行为逐字保持不变。
func (x *Executor) enforceKeeperRules(ctx context.Context, items []Item) ([]Item, keeper.Stats, int, error) {
	protected, keeperPaths, stats, err := keeper.Enforce(ctx, x.st)
	if err != nil {
		return items, stats, 0, fmt.Errorf("同组保留条件判定失败，已阻止执行: %w", err)
	}
	if len(protected) == 0 {
		return items, stats, 0, nil
	}

	out := make([]Item, 0, len(items))
	skipped := 0
	for _, it := range items {
		if protected[it.FileID] {
			// 落到明细里而不是静默丢弃：用户在清理任务页能看到「为什么没删」
			if _, e := x.st.Exec(ctx,
				`UPDATE clean_items SET selected=0, action='skipped', error=?, executed_at=?
				 WHERE id=? AND action='pending'`,
				"按同组保留条件判定为保留文件，已跳过删除", store.NowMS(), it.ID); e != nil {
				return items, stats, skipped, e
			}
			skipped++
			continue
		}
		// 保留副本可能刚被条件重算换过，同步刷新参照路径，
		// 保证 sampled 分组的「删除前逐字节比对」拿的是新保留的那份
		if p, ok := keeperPaths[it.GroupID]; ok && p != "" {
			it.KeeperPath = p
		}
		out = append(out, it)
	}
	return out, stats, skipped, nil
}
