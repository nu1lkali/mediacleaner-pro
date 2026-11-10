import { useEffect, useMemo, useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  CheckCheck,
  ChevronLeft,
  ChevronRight,
  Copy,
  Crown,
  Download,
  Eraser,
  FileSpreadsheet,
  Film,
  FolderOpen,
  Images,
  Layers,
  RefreshCw,
  ScanLine,
  Search,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import { api } from "../services/api";
import type {
  DupGroup,
  GroupKind,
  GroupMember,
  KeeperRuleDef,
  KeeperRuleStats,
  ScanBatch,
  TrustAllResult,
} from "../services/types";
import { Thumb, Preview } from "../components/Thumb";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Field, Select, Input } from "../components/ui/field";
import { ConfirmDialog, Dialog } from "../components/ui/dialog";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui/empty";
import {
  cn,
  formatBytes,
  formatClock,
  formatDate,
  formatNumber,
  groupKindLabel,
  kindLabel,
  resolutionLabel,
} from "../lib/utils";

const KIND_META: Record<
  string,
  { title: string; desc: string; icon: React.ReactNode; tone: "info" | "success" | "warn" }
> = {
  exact: {
    title: "完全重复",
    desc: "把内容相同的多个副本归为一组，每组保留 1 份。判定强度取决于扫描时的选择：全文校验 / 清理前逐字节复验 / 免校验（不比对）。",
    icon: <Copy className="h-4 w-4" />,
    tone: "info",
  },
  video: {
    title: "相似视频",
    desc: "关键帧指纹匹配的视频：转码、换封装、改码率、裁边的同一部片子会落进同一组。",
    icon: <Film className="h-4 w-4" />,
    tone: "warn",
  },
  image: {
    title: "相似图片",
    desc: "感知哈希（pHash）聚类出的同图不同尺寸/压缩/水印版本。",
    icon: <Images className="h-4 w-4" />,
    tone: "success",
  },
};

export default function GroupsPage({ kind }: { kind: GroupKind }) {
  const qc = useQueryClient();
  const meta = KIND_META[kind] ?? KIND_META.exact;

  const [page, setPage] = useState(1);
  const [size] = useState(20);
  const [sort, setSort] = useState("reclaim");
  const [minReclaim, setMinReclaim] = useState("");
  const [keyword, setKeyword] = useState("");
  const [selected, setSelected] = useState<Map<number, GroupMember>>(new Map());
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [quickOpen, setQuickOpen] = useState(false);
  const [createdTaskId, setCreatedTaskId] = useState<number | null>(null);
  const [detail, setDetail] = useState<GroupMember | null>(null);
  const [exportOpen, setExportOpen] = useState(false);
  // 保留条件：草稿值只在用户改动下拉时存在，否则以服务器保存的条件为准
  const [ruleDraft, setRuleDraft] = useState<string | null>(null);
  const [ruleResult, setRuleResult] = useState<KeeperRuleStats | null>(null);
  // 批次切换 / 一键信任未校验分组
  const [trustOpen, setTrustOpen] = useState(false);
  const [trustResult, setTrustResult] = useState<string | null>(null);

  useEffect(() => {
    setRuleDraft(null);
    setRuleResult(null);
    setTrustResult(null);
  }, [kind]);

  const batches = useQuery({ queryKey: ["batches"], queryFn: api.batches });
  const activeTaskId = batches.data?.active_task_id ?? 0;
  const activeBatch = batches.data?.items.find((b: ScanBatch) => b.active) ?? null;
  const unverifiedCount = activeBatch?.unverified ?? 0;
  const trustedCount = activeBatch?.trusted ?? 0;

  const switchBatch = useMutation({
    mutationFn: (id: number) => api.switchBatch(id),
    onSuccess: () => {
      setSelected(new Map());
      setRuleResult(null);
      setTrustResult(null);
      qc.invalidateQueries({ queryKey: ["batches"] });
      qc.invalidateQueries({ queryKey: ["groups"] });
      qc.invalidateQueries({ queryKey: ["group-summary"] });
      qc.invalidateQueries({ queryKey: ["keeper-rule"] });
    },
  });

  const trustAll = useMutation({
    mutationFn: (restore: boolean) => api.trustAll(restore),
    onSuccess: (res: TrustAllResult) => {
      setTrustOpen(false);
      setTrustResult(
        res.restore
          ? `已撤销 ${res.changed} 组的免校验，清理前会重新与保留副本逐字节复验。`
          : `已把 ${res.changed} 组「未校验」标为免校验，清理时不再比对内容。`,
      );
      qc.invalidateQueries({ queryKey: ["batches"] });
      qc.invalidateQueries({ queryKey: ["groups"] });
      qc.invalidateQueries({ queryKey: ["group-summary"] });
    },
  });

  const summary = useQuery({ queryKey: ["group-summary", activeTaskId], queryFn: api.groupSummary });
  const keeperRule = useQuery({
    queryKey: ["keeper-rule", kind, activeTaskId],
    queryFn: () => api.keeperRule(kind),
  });
  const groups = useQuery({
    // 批次 ID 进 key：切换批次后不会短暂显示上一批的缓存数据
    queryKey: ["groups", kind, page, sort, minReclaim, activeTaskId],
    queryFn: () =>
      api.groups({
        kind,
        page,
        size,
        sort,
        min_reclaim: minReclaim ? Number(minReclaim) * 1024 * 1024 : undefined,
      }),
    placeholderData: keepPreviousData,
  });

  const setKeeper = useMutation({
    mutationFn: ({ gid, fid }: { gid: number; fid: number }) => api.setKeeper(gid, fid),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["groups"] });
      qc.invalidateQueries({ queryKey: ["group-summary"] });
    },
  });

  const invalidateAfterClean = (id: number) => {
    setSelected(new Map());
    setConfirmOpen(false);
    setQuickOpen(false);
    setCreatedTaskId(id);
    qc.invalidateQueries({ queryKey: ["clean-tasks"] });
    qc.invalidateQueries({ queryKey: ["dashboard"] });
    qc.invalidateQueries({ queryKey: ["groups"] });
    qc.invalidateQueries({ queryKey: ["group-summary"] });
  };

  // 手动勾选：创建任务后立即执行，避免「点了没反应，还要自己跑去清理页点执行」。
  const clean = useMutation({
    mutationFn: async () => {
      const task = await api.createCleanTask({
        name: `手动清理 · ${meta.title}`,
        file_ids: [...selected.keys()],
        execute_mode: "quarantine",
      });
      await api.executeClean(task.id);
      return task.id;
    },
    onSuccess: invalidateAfterClean,
  });

  // 一键清理：由后端把该类型全部分组的非保留副本纳入计划（前端只加载了当前页）。
  const quickClean = useMutation({
    mutationFn: async () => {
      const task = await api.createCleanTask({
        name: `一键清理 · ${meta.title}`,
        group_kind: kind,
        execute_mode: "quarantine",
      });
      await api.executeClean(task.id);
      return task.id;
    },
    onSuccess: invalidateAfterClean,
  });

  // 显式标注：本机 node_modules 的 react-query 类型不完整时，避免推导成 any
  const defs: KeeperRuleDef[] = keeperRule.data?.definitions ?? [];
  const savedRule: string = keeperRule.data?.rule ?? "";
  const rule: string = ruleDraft ?? savedRule;
  const ruleDef: KeeperRuleDef | undefined = defs.find((d: KeeperRuleDef) => d.id === rule);
  const ruleLabel = (id: string): string =>
    defs.find((d: KeeperRuleDef) => d.id === id)?.label ?? id;
  const btimeRatio = keeperRule.data?.btime_total
    ? Math.round(((keeperRule.data?.btime_known ?? 0) / keeperRule.data.btime_total) * 100)
    : 0;

  // 一键智能设定保留项；group_id 存在时只重算单组，不改整类默认条件
  const applyRule = useMutation({
    mutationFn: (body: { rule: string; group_id?: number }) =>
      api.applyKeeperRule({ kind, rule: body.rule, group_id: body.group_id }),
    onSuccess: (res: { stats: KeeperRuleStats }) => {
      setRuleDraft(null);
      setRuleResult(res.stats);
      // keeper 可能整体换过，旧勾选里会混进新的保留文件，直接清空最安全
      setSelected(new Map());
      qc.invalidateQueries({ queryKey: ["keeper-rule"] });
      qc.invalidateQueries({ queryKey: ["groups"] });
      qc.invalidateQueries({ queryKey: ["group-summary"] });
    },
  });

  const items = useMemo(() => {
    const list = groups.data?.items ?? [];
    if (!keyword.trim()) return list;
    const kw = keyword.trim().toLowerCase();
    return list.filter(
      (g) => g.signature.toLowerCase().includes(kw) || g.members.some((m) => m.name.toLowerCase().includes(kw)),
    );
  }, [groups.data, keyword]);

  const totalPages = Math.max(1, Math.ceil((groups.data?.total ?? 0) / size));
  const selectedBytes = [...selected.values()].reduce((s, m) => s + m.size, 0);
  const sumItem = summary.data?.items.find((x) => x.kind === kind);

  const toggle = (m: GroupMember) => {
    setSelected((prev) => {
      const next = new Map(prev);
      if (next.has(m.file_id)) next.delete(m.file_id);
      else next.set(m.file_id, m);
      return next;
    });
  };

  const selectAllOnPage = () => {
    setSelected((prev) => {
      const next = new Map(prev);
      for (const g of items) {
        for (const m of g.members) {
          if (!m.is_keeper && m.state === "active") next.set(m.file_id, m);
        }
      }
      return next;
    });
  };

  return (
    <div className="pb-10">
      <PageHeader
        title={meta.title}
        desc={meta.desc}
        actions={
          <>
            <div className="relative w-full sm:w-auto">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
                placeholder="搜索文件名/签名"
                className="h-8 w-full pl-8 text-[12px] sm:w-[190px]"
              />
            </div>
            <Select
              value={sort}
              onChange={(e) => { setSort(e.target.value); setPage(1); }}
              className="h-9 min-w-[176px] text-[13px] sm:w-[190px]"
              aria-label="分组排序方式"
            >
              <option value="reclaim">按可回收排序</option>
              <option value="size">按文件体积</option>
              <option value="members">按成员数量</option>
              <option value="similarity">按相似度</option>
              <option value="newest">按发现时间</option>
            </Select>
            <Input
              value={minReclaim}
              onChange={(e) => { setMinReclaim(e.target.value); setPage(1); }}
              placeholder="最小 MB"
              className="h-8 flex-1 text-[12px] sm:w-[92px] sm:flex-none"
              inputMode="numeric"
            />
            <Button
              size="sm"
              variant="outline"
              icon={<Download className="h-3.5 w-3.5" />}
              onClick={() => setExportOpen(true)}
            >
              导出清单
            </Button>
          </>
        }
      />

      <div className="space-y-4 px-4 sm:px-5">
        {/* 结果批次：每次扫描各留一份独立结果，可切换查看 */}
        <Card className="border-primary/20">
          <CardBody className="space-y-2.5 p-3">
            <div className="flex flex-wrap items-center gap-2">
              <span className="flex shrink-0 items-center gap-1.5 text-[12.5px] font-medium">
                <Layers className="h-3.5 w-3.5 text-primary" />
                当前结果批次
              </span>
              <Select
                value={activeTaskId || ""}
                onChange={(e) => switchBatch.mutate(Number(e.target.value))}
                disabled={switchBatch.isPending || (batches.data?.items.length ?? 0) === 0}
                className="h-9 min-w-[240px] flex-1 text-[12.5px] sm:max-w-[520px]"
                aria-label="切换结果批次"
              >
                {(batches.data?.items.length ?? 0) === 0 && <option value="">暂无扫描结果</option>}
                {(batches.data?.items ?? []).map((b: ScanBatch) => (
                  <option key={b.scan_task_id} value={b.scan_task_id}>
                    #{b.scan_task_id} {b.name} · {formatNumber(b.groups)} 组 / 可删 {formatNumber(b.removable)}
                    {b.active ? "（当前）" : ""}
                  </option>
                ))}
              </Select>
              {switchBatch.isPending && <span className="text-[11px] text-muted-foreground">切换中…</span>}
            </div>

            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-muted-foreground">
              <span>每次扫描各留一份结果、互不混合；切换批次不会重新扫描。</span>
              <span>已校验 <b className="text-foreground">{formatNumber(activeBatch?.verified ?? 0)}</b> 组</span>
              <span>未校验 <b className="text-foreground">{formatNumber(unverifiedCount)}</b> 组</span>
              {trustedCount > 0 && (
                <span className="text-destructive">免校验 <b>{formatNumber(trustedCount)}</b> 组</span>
              )}
            </div>

            {(unverifiedCount > 0 || trustedCount > 0) && (
              <div className="flex flex-wrap items-center gap-2">
                {unverifiedCount > 0 && (
                  <Button
                    size="sm"
                    variant="outline"
                    icon={<CheckCheck className="h-3.5 w-3.5" />}
                    onClick={() => setTrustOpen(true)}
                  >
                    一键信任全部未校验分组（{formatNumber(unverifiedCount)}）
                  </Button>
                )}
                {trustedCount > 0 && (
                  <Button
                    size="sm"
                    variant="ghost"
                    icon={<RefreshCw className="h-3.5 w-3.5" />}
                    disabled={trustAll.isPending}
                    onClick={() => trustAll.mutate(true)}
                  >
                    撤销免校验（{formatNumber(trustedCount)}）
                  </Button>
                )}
                <span className="text-[11px] text-muted-foreground">
                  {unverifiedCount > 0
                    ? "标记后这些分组可直接批量删除，不再逐字节复验。"
                    : "免校验分组在清理时不会比对内容。"}
                </span>
              </div>
            )}

            {trustResult && (
              <div className="rounded-md border border-success/30 bg-success/8 px-3 py-2 text-[12px] text-success">
                {trustResult}
              </div>
            )}
            {(batches.isError || switchBatch.isError || trustAll.isError) && (
              <div className="text-[12px] text-destructive">
                {((batches.error ?? switchBatch.error ?? trustAll.error) as Error | null)?.message}
              </div>
            )}
          </CardBody>
        </Card>

        {/* 汇总条 */}
        <div className="grid gap-3 sm:grid-cols-3">
          <MiniStat icon={<Layers className="h-4 w-4" />} label="分组数" value={formatNumber(sumItem?.groups ?? 0)} />
          <MiniStat icon={<Copy className="h-4 w-4" />} label="可删副本" value={formatNumber(sumItem?.removable ?? 0)} />
          <MiniStat icon={<Sparkles className="h-4 w-4" />} label="可释放空间" value={formatBytes(sumItem?.bytes ?? 0)} />
        </div>

        {/* 快捷批量清理：不需要重新全文扫描，创建任务后立即执行 */}
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-primary/20 bg-primary/[0.04] px-3 py-2.5">
          <div className="min-w-0 flex-1 basis-full text-[12px] leading-relaxed text-muted-foreground sm:basis-auto">
            共 <b className="text-foreground">{formatNumber(sumItem?.removable ?? 0)}</b> 个可删副本 · 预计释放{" "}
            <b className="text-foreground">{formatBytes(sumItem?.bytes ?? 0)}</b>，每组自动保留 1 份
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              variant="outline"
              icon={<CheckCheck className="h-3.5 w-3.5" />}
              onClick={selectAllOnPage}
              disabled={items.length === 0}
            >
              全选本页
            </Button>
            <Button
              size="sm"
              variant="primary"
              icon={<Eraser className="h-3.5 w-3.5" />}
              onClick={() => setQuickOpen(true)}
              disabled={!sumItem?.removable}
            >
              一键清理全部{meta.title}
            </Button>
          </div>
        </div>

        {/* 同组保留条件：条件按「分组类型」持久化，可一键应用到整类或单组 */}
        <Card className="border-primary/20">
          <CardHeader
            className="flex-col items-stretch gap-2 bg-muted/30 py-2.5 sm:flex-row sm:items-start sm:justify-between"
            action={
              <Badge tone={savedRule ? "success" : "outline"} className="whitespace-nowrap">
                {savedRule ? `已配置：${ruleLabel(savedRule)}` : "未配置 · 保持原有行为"}
              </Badge>
            }
          >
            <CardTitle hint="在同一组重复项里指定保留哪一份；未配置时完全沿用原有的手动/默认保留逻辑。">
              同组保留条件
            </CardTitle>
          </CardHeader>
          <CardBody className="space-y-3 p-3 sm:p-4">
            <div className="flex flex-wrap items-center gap-2">
              <Select
                value={rule}
                onChange={(e) => { setRuleDraft(e.target.value); setRuleResult(null); }}
                className="h-8 w-full text-[12px] sm:w-[210px]"
                aria-label="同组保留条件"
              >
                <option value="">未配置（保持原有去重行为）</option>
                {defs.map((d) => (
                  <option key={d.id} value={d.id}>{d.label}</option>
                ))}
              </Select>
              <Button
                size="sm"
                variant="primary"
                icon={<Sparkles className="h-3.5 w-3.5" />}
                loading={applyRule.isPending}
                disabled={!rule || keeperRule.isLoading}
                onClick={() => applyRule.mutate({ rule })}
              >
                一键智能设定保留项
              </Button>
              {savedRule && (
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={applyRule.isPending}
                  onClick={() => applyRule.mutate({ rule: "" })}
                >
                  清除条件
                </Button>
              )}
              {ruleDraft !== null && ruleDraft !== savedRule && (
                <span className="text-[11px] text-warning">尚未应用</span>
              )}
            </div>

            <div className="rounded-md border border-border bg-muted/30 px-3 py-2 text-[11.5px] leading-relaxed text-muted-foreground">
              {ruleDef ? (
                <>
                  <div><b className="text-foreground">判定口径：</b>{ruleDef.criteria}</div>
                  <div className="mt-1"><b className="text-foreground">适用范围：</b>{ruleDef.scope} 数据源 {ruleDef.field}。</div>
                </>
              ) : (
                <div><b className="text-foreground">判定口径：</b>不干预。沿用扫描时的默认保留（路径层级最浅 → 路径字典序最小）或用户手动指定的保留文件。</div>
              )}
              <div className="mt-1">
                <b className="text-foreground">执行时序：</b>执行清理前会先按条件重算组内保留文件，再从待删清单剔除被保留的文件，只删未被保留的副本；并列时按路径字典序取最小，结果确定可复现。
              </div>
              {ruleDef?.needs_btime && (
                <div className="mt-1 text-warning">
                  创建时间可用率 {keeperRule.data?.btime_known ?? 0}/{keeperRule.data?.btime_total ?? 0}（{btimeRatio}%）。
                  {btimeRatio === 0
                    ? "该卷未提供出生时间，条件将回退原有保留项；可改用「最早/最晚修改」。"
                    : "未知创建时间的成员不参与比较，也不会被选为保留文件。"}
                </div>
              )}
              <div className="mt-1">
                在某组手动点「保留」会解除该组的条件（手动优先）；条件按类型保存，重新扫描后自动恢复。
                清除条件只移除配置，不会重算已有的保留文件。
              </div>
            </div>

            {ruleResult && (
              <div className="space-y-1 rounded-md border border-success/30 bg-success/8 px-3 py-2 text-[12px] leading-relaxed text-success">
                <div>
                  已{ruleResult.rule ? `应用「${ruleLabel(ruleResult.rule)}」` : "清除条件"}：
                  共 <b>{ruleResult.groups}</b> 组，每组保留 1 份 → <b>{ruleResult.protected}</b> 个保留文件。
                  这些文件在任何情况下都不会进入清理任务，不是「从待删清单里拦下来的」。
                </div>
                <div>
                  其中 <b>{ruleResult.changed}</b> 组换了「由哪一份来保留」，
                  其余 {Math.max(ruleResult.groups - ruleResult.changed, 0)} 组原本就符合条件、未改动。
                  可删副本数量不变，只是留下的那一份不同。
                </div>
                {ruleResult.unavailable > 0 && (
                  <div className="text-warning">
                    {ruleResult.unavailable} 组创建时间不可用，已回退原有保留项
                  </div>
                )}
              </div>
            )}
            {(keeperRule.isError || applyRule.isError) && (
              <div className="text-[12px] text-destructive">
                {((keeperRule.error ?? applyRule.error) as Error | null)?.message}
              </div>
            )}
          </CardBody>
        </Card>

        {groups.isLoading ? (
          <Loading />
        ) : groups.error ? (
          <ErrorBox message={(groups.error as Error).message} onRetry={() => groups.refetch()} />
        ) : items.length === 0 ? (
          <Card>
            <EmptyState
              icon={<ScanLine className="h-5 w-5" />}
              title={`暂无${meta.title}分组`}
              desc={
                kind === "exact"
                  ? "先运行一次「极速：仅完全重复」扫描，几秒到几分钟即可出结果。"
                  : kind === "video"
                    ? "相似视频需要 ffmpeg 抽帧，请使用「深度：含相似视频」预设重新扫描。"
                    : "相似图片需要「标准」或更高级别预设。"
              }
              action={
                <Button variant="primary" size="sm" onClick={() => (window.location.href = "/scan")}>
                  去扫描
                </Button>
              }
            />
          </Card>
        ) : (
          <div className="space-y-3">
            {items.map((g) => (
              <GroupCard
                key={g.id}
                group={g}
                selected={selected}
                onToggle={toggle}
                onSetKeeper={(fid) => setKeeper.mutate({ gid: g.id, fid })}
                keeperBusy={setKeeper.isPending}
                onPreview={setDetail}
                ruleLabel={ruleLabel}
                activeRule={rule}
                onReapplyRule={(gid) => applyRule.mutate({ rule, group_id: gid })}
                ruleBusy={applyRule.isPending}
              />
            ))}
          </div>
        )}

        {totalPages > 1 && (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-card px-3 py-2">
            <div className="text-[12px] text-muted-foreground">
              共 {formatNumber(groups.data?.total ?? 0)} 组 · 第 {page}/{totalPages} 页
            </div>
            <div className="flex items-center gap-1.5">
              <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
                <ChevronLeft className="h-3.5 w-3.5" /> 上一页
              </Button>
              <Button size="sm" variant="outline" disabled={page >= totalPages} onClick={() => setPage((p) => p + 1)}>
                下一页 <ChevronRight className="h-3.5 w-3.5" />
              </Button>
            </div>
          </div>
        )}
      </div>

      {/* 底部批量操作条：小屏换行为两行，避免元素互相挤压 */}
      {selected.size > 0 && (
        <div className="fixed bottom-3 left-1/2 z-40 flex w-[calc(100vw-1.5rem)] max-w-xl -translate-x-1/2 flex-wrap items-center justify-between gap-2 rounded-xl border border-border bg-card px-3 py-2.5 shadow-lift sm:bottom-5 sm:w-auto sm:flex-nowrap sm:gap-4">
          <div className="min-w-0 text-[12.5px]">
            已选 <span className="font-semibold text-primary">{selected.size}</span> 个文件 ·{" "}
            <span className="tabular-nums">{formatBytes(selectedBytes)}</span>
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            <Button size="sm" variant="ghost" onClick={() => setSelected(new Map())}>
              清空
            </Button>
            <Button
              size="sm"
              variant="destructive"
              icon={<Eraser className="h-3.5 w-3.5" />}
              onClick={() => setConfirmOpen(true)}
            >
              移入隔离区
            </Button>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={trustOpen}
        onClose={() => setTrustOpen(false)}
        onConfirm={() => trustAll.mutate(false)}
        loading={trustAll.isPending}
        title={`把 ${formatNumber(unverifiedCount)} 组「未校验」标记为免校验？`}
        confirmText="我已了解风险，全部标记"
        danger
        desc={
          <>
            这<b>不会重新全文扫描</b>，只是跳过清理前的逐字节比对，让这些分组可以直接批量删除
            （即之前提到的「标为已校验」效果）。
            <div className="mt-2 text-destructive">
              风险：这些分组只比过文件大小与 4 段采样指纹。若存在大小相同、采样段也相同、
              但内容不同的文件，清理时不会再把它们拦下来，可能被当成副本删除。
            </div>
            <div className="mt-2">
              只作用于当前批次{activeTaskId ? ` #${activeTaskId}` : ""}，其它批次不受影响；
              随时可点「撤销免校验」恢复比对，操作会写入审计日志。
            </div>
            <div className="mt-2">
              文件仍会先移入隔离区，保留期内可恢复；每组至少保留 1 份，且会确认保留副本仍然存在。
            </div>
            {trustAll.isError && (
              <div className="mt-2 text-destructive">{(trustAll.error as Error).message}</div>
            )}
          </>
        }
      />

      <ConfirmDialog
        open={confirmOpen}
        onClose={() => setConfirmOpen(false)}
        onConfirm={() => clean.mutate()}
        loading={clean.isPending}
        title="批量移入隔离区？"
        confirmText="立即执行清理"
        danger
        desc={
          <>
            共 <b>{selected.size}</b> 个文件、<b>{formatBytes(selectedBytes)}</b>。确认后会创建任务并立即执行，
            文件移动到隔离区而不是立即删除，可在保留期内随时恢复。每组的保留文件不会被移除；
            标注「未校验」的分组在删除前会与保留副本逐字节比对，不一致会跳过。
            {trustedCount > 0 && (
              <div className="mt-2 text-destructive">
                注意：当前批次有 {formatNumber(trustedCount)} 组标为「免校验」，涉及这些组的副本清理时不会比对内容。
              </div>
            )}
            {savedRule && (
              <div className="mt-2">
                已配置「{ruleLabel(savedRule)}」：执行前会先按条件重算组内保留文件，仅删除未被保留的副本。
              </div>
            )}
            {clean.isError && (
              <div className="mt-2 text-destructive">{(clean.error as Error).message}</div>
            )}
          </>
        }
      />

      <ConfirmDialog
        open={quickOpen}
        onClose={() => setQuickOpen(false)}
        onConfirm={() => quickClean.mutate()}
        loading={quickClean.isPending}
        title={`一键清理全部${meta.title}？`}
        confirmText="创建并立即执行"
        danger
        desc={
          <>
            将把 <b>全部{meta.title}分组</b>里的非保留副本（预计 {formatNumber(sumItem?.removable ?? 0)} 个、
            {formatBytes(sumItem?.bytes ?? 0)}）一次性纳入清理任务并立即执行，每组保留 1 份。
            <div className="mt-2">
              文件进入隔离区、保留期内可恢复；标为「未校验」的分组会先与保留副本逐字节比对，
              不一致的会被跳过，不需要重新全文扫描。
            </div>
            {trustedCount > 0 && (
              <div className="mt-2 text-destructive">
                当前批次有 {formatNumber(trustedCount)} 组标为「免校验」，这些组清理时不会比对内容，
                存在同大小不同内容被误删的风险。
              </div>
            )}
            {savedRule && (
              <div className="mt-2">
                执行前先按「{ruleLabel(savedRule)}」逐组重算保留文件，被判为保留的会自动从待删清单剔除，
                每组至少留 1 份。
              </div>
            )}
            {kind !== "exact" && (
              <div className="mt-2 text-warning">
                注意：相似分组的内容并非完全相同，删除前建议先抽查预览，确认画质与时长可接受。
              </div>
            )}
            {quickClean.isError && (
              <div className="mt-2 text-destructive">{(quickClean.error as Error).message}</div>
            )}
          </>
        }
      />

      {createdTaskId !== null && (
        <Dialog
          open
          onClose={() => setCreatedTaskId(null)}
          title="清理任务已开始执行"
          desc={`任务 #${createdTaskId} 正在后台运行`}
          width="max-w-sm"
          footer={
            <>
              <Button size="sm" variant="ghost" onClick={() => setCreatedTaskId(null)}>
                关闭
              </Button>
              <Button size="sm" variant="primary" onClick={() => (window.location.href = "/clean")}>
                查看执行进度
              </Button>
            </>
          }
        >
          <div className="space-y-2 text-[12.5px] leading-relaxed text-muted-foreground">
            <div>文件先移入隔离区，可在保留期内恢复；执行进度与逐字节校验结果会在清理任务页实时显示。</div>
            <div>若某个「未校验」分组与保留副本内容不一致，该副本会被自动跳过，不会被删除。</div>
          </div>
        </Dialog>
      )}

      <FileDetailDialog member={detail} onClose={() => setDetail(null)} />

      {exportOpen && <GroupsExportDialog kind={kind} label={meta.title} onClose={() => setExportOpen(false)} />}
    </div>
  );
}

function GroupsExportDialog({ kind, label, onClose }: { kind: GroupKind; label: string; onClose: () => void }) {
  const [format, setFormat] = useState<"csv" | "xlsx" | "json" | "html">("xlsx");
  const [result, setResult] = useState<{ file: string; rows: number; download_url: string } | null>(null);

  const run = useMutation({
    mutationFn: () => api.exportData({ kind: "groups", format, kind_filter: kind }),
    onSuccess: (r) => setResult(r),
  });

  return (
    <Dialog
      open
      onClose={onClose}
      title={`导出 ${label} 清单`}
      desc="按分组导出全部成员的路径、大小与相似度，可用于离线核对"
      width="max-w-md"
      footer={
        <>
          <Button size="sm" variant="ghost" onClick={onClose}>
            关闭
          </Button>
          {result ? (
            <a href={api.downloadUrl(result.download_url)} download>
              <Button size="sm" variant="primary" icon={<Download className="h-3.5 w-3.5" />}>
                下载 {result.file}
              </Button>
            </a>
          ) : (
            <Button size="sm" variant="primary" loading={run.isPending} onClick={() => run.mutate()}>
              生成文件
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-3">
        <Field label="格式">
          <Select value={format} onChange={(e) => setFormat(e.target.value as typeof format)}>
            <option value="xlsx">Excel (.xlsx) — 带样式，适合归档</option>
            <option value="csv">CSV — 通用，可再加工</option>
            <option value="json">JSON — 程序化处理</option>
            <option value="html">HTML — 直接分享查看</option>
          </Select>
        </Field>
        {run.isError && <div className="text-[12px] text-destructive">{(run.error as Error).message}</div>}
        {result && (
          <div className="flex items-center gap-2 rounded-md border border-success/30 bg-success/8 px-3 py-2 text-[12px] text-success">
            <FileSpreadsheet className="h-4 w-4" />
            已生成 {result.rows} 行 · {result.file}
          </div>
        )}
      </div>
    </Dialog>
  );
}

function MiniStat({ icon, label, value }: { icon: React.ReactNode; label: string; value: string }) {
  return (
    <Card className="flex items-center gap-3 px-4 py-3">
      <div className="rounded-md bg-primary/10 p-2 text-primary">{icon}</div>
      <div>
        <div className="text-[11px] text-muted-foreground">{label}</div>
        <div className="text-[16px] font-semibold tabular-nums">{value}</div>
      </div>
    </Card>
  );
}

function GroupCard({
  group,
  selected,
  onToggle,
  onSetKeeper,
  keeperBusy,
  onPreview,
  ruleLabel,
  activeRule,
  onReapplyRule,
  ruleBusy,
}: {
  group: DupGroup;
  selected: Map<number, GroupMember>;
  onToggle: (m: GroupMember) => void;
  onSetKeeper: (fileId: number) => void;
  keeperBusy: boolean;
  onPreview: (m: GroupMember) => void;
  ruleLabel: (id: string) => string;
  activeRule: string;
  onReapplyRule: (groupId: number) => void;
  ruleBusy: boolean;
}) {
  const [expanded, setExpanded] = useState(group.member_count <= 4);
  const shown = expanded ? group.members : group.members.slice(0, 3);
  const keeper = group.members.find((m) => m.is_keeper);

  return (
    <Card className="overflow-hidden">
      {/* 小屏：标题与徽标上下堆叠；徽标行可换行，杜绝窄屏互相重叠 */}
      <CardHeader
        className="flex-col items-stretch gap-2 bg-muted/30 py-2.5 sm:flex-row sm:items-start sm:justify-between"
        action={
          <div className="flex flex-wrap items-center gap-1.5 sm:justify-end sm:gap-2">
            {group.evidence === "sampled" && (
              <span
                className="shrink-0"
                title="仅按大小 + 采样指纹判定，执行清理前会自动与保留副本逐字节比对"
              >
                <Badge tone="warn" className="whitespace-nowrap">未校验</Badge>
              </span>
            )}
            {group.evidence === "trusted" && (
              <span
                className="shrink-0"
                title="用户确认免校验：清理时不会与保留副本逐字节比对"
              >
                <Badge tone="danger" className="whitespace-nowrap">免校验</Badge>
              </span>
            )}
            {group.kind !== "exact" && group.avg_similarity > 0 && (
              <Badge tone="outline" className="whitespace-nowrap">
                相似度 {(group.avg_similarity * 100).toFixed(1)}%
              </Badge>
            )}
            <Badge tone="info" className="whitespace-nowrap">{group.member_count} 个文件</Badge>
            <Badge tone="warn" className="whitespace-nowrap">可释放 {formatBytes(group.reclaimable_bytes)}</Badge>
          </div>
        }
      >
        <div className="flex min-w-0 items-center gap-2">
          <span className="mono shrink-0 text-[11px] text-muted-foreground">#{group.id}</span>
          <div className="min-w-0 flex-1">
            <CardTitle
              hint={
                keeper
                  ? `保留：${keeper.name}`
                  : `单文件 ${formatBytes(group.size)} · ${groupKindLabel(group.kind)}`
              }
            >
              {group.signature || `${group.member_count} 个副本`}
            </CardTitle>
          </div>
        </div>
      </CardHeader>

      {group.evidence === "sampled" && (
        <div className="border-t border-amber-200 bg-amber-50 px-4 py-2 text-[11.5px] leading-relaxed text-amber-800">
          高度可能重复：只比过文件大小与 4 段采样，未读全文。执行清理时会自动与保留副本逐字节比对，
          内容不一致会拒绝删除；拿不准可先预览播放一下。
        </div>
      )}

      {group.evidence === "trusted" && (
        <div className="flex items-start gap-2 border-t border-destructive/25 bg-destructive/[0.06] px-4 py-2 text-[11.5px] leading-relaxed text-destructive">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            免校验：只比过大小与 4 段采样，清理时不会与保留副本逐字节比对。
            若内容与保留副本其实不同，仍会被删除；请确认后再执行清理，或在上方的批次栏点「撤销免校验」。
          </span>
        </div>
      )}

      {(group.keeper_rule || activeRule) && (
        <div className="flex flex-wrap items-center gap-2 border-t border-border/60 bg-muted/20 px-3 py-1.5 text-[11px]">
          <Badge tone={group.keeper_rule ? "success" : "outline"} className="shrink-0">
            {group.keeper_rule ? `保留条件：${ruleLabel(group.keeper_rule)}` : "该组未配置保留条件"}
          </Badge>
          <span className="min-w-0 flex-1 truncate text-muted-foreground" title={keeper?.keep_reason}>
            {keeper
              ? `当前保留：${keeper.name}${keeper.keep_reason ? ` · ${keeper.keep_reason}` : ""}`
              : "尚未确定保留文件"}
          </span>
          <Button
            size="sm"
            variant="ghost"
            className="shrink-0 text-[11px]"
            icon={<RefreshCw className="h-3 w-3" />}
            disabled={!activeRule || ruleBusy}
            onClick={() => onReapplyRule(group.id)}
            title="仅对本组按当前所选条件重新判定保留文件"
          >
            按条件重选
          </Button>
        </div>
      )}

      <div className="divide-y divide-border/60">
        {shown.map((m) => {
          const checked = selected.has(m.file_id);
          return (
            <div
              key={m.file_id}
              className={cn(
                "flex items-center gap-3 px-3 py-2 transition-colors",
                checked && "bg-destructive/[0.045]",
                m.is_keeper && "bg-success/[0.04]",
              )}
            >
              {m.is_keeper ? (
                <span className="w-4 shrink-0 text-success" title="该文件将被保留">
                  <ShieldCheck className="h-4 w-4" />
                </span>
              ) : (
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={() => onToggle(m)}
                  className="h-3.5 w-3.5 shrink-0 accent-destructive"
                />
              )}

              <button
                onClick={() => onPreview(m)}
                className="h-10 w-14 shrink-0 overflow-hidden rounded border border-border bg-muted"
                title="查看详情"
              >
                <Thumb fileId={m.file_id} kind={m.media_kind} width={120} className="h-full w-full" />
              </button>

              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-1.5">
                  <span className="truncate text-[12.5px] font-medium">{m.name}</span>
                  {m.is_keeper && <Badge tone="success" className="shrink-0">保留</Badge>}
                  {m.similarity > 0 && m.similarity < 1 && (
                    <Badge tone="outline" className="shrink-0">{(m.similarity * 100).toFixed(0)}%</Badge>
                  )}
                </div>
                <div className="mt-0.5 flex items-center gap-1.5 truncate text-[11px] text-muted-foreground">
                  <FolderOpen className="h-3 w-3 shrink-0" />
                  <span className="truncate" title={m.path}>{m.path}</span>
                </div>
              </div>

              <div className="hidden shrink-0 items-center gap-3 text-[11px] text-muted-foreground sm:flex">
                <span className="w-[68px] text-right tabular-nums">{formatBytes(m.size)}</span>
                <span className="w-[58px] text-right">{resolutionLabel(m.width, m.height)}</span>
                <span className="w-[52px] text-right tabular-nums">
                  {m.duration_ms ? formatClock(m.duration_ms) : "—"}
                </span>
                <span className="w-[86px] text-right tabular-nums">{formatDate(m.mtime_ns / 1e6, false)}</span>
              </div>

              {/* 尾部操作位：保留项没有「设为保留」按钮，但必须占住同样的宽度，
                  否则 flex-1 的名称区吸收的剩余空间不同，两行的元数据列会错位 */}
              {m.is_keeper ? (
                <span className="invisible shrink-0" aria-hidden="true">
                  <Button size="sm" variant="ghost" className="text-[11px]" icon={<Crown className="h-3 w-3" />}>
                    保留
                  </Button>
                </span>
              ) : (
                <Button
                  size="sm"
                  variant="ghost"
                  className="shrink-0 text-[11px]"
                  icon={<Crown className="h-3 w-3" />}
                  disabled={keeperBusy}
                  onClick={() => onSetKeeper(m.file_id)}
                  title="设为保留文件（该组其余文件才可清理）"
                >
                  保留
                </Button>
              )}
            </div>
          );
        })}
      </div>

      {group.members.length > 3 && (
        <button
          onClick={() => setExpanded((v) => !v)}
          className="w-full border-t border-border/60 py-1.5 text-[11.5px] text-muted-foreground hover:bg-muted/50"
        >
          {expanded ? "收起" : `展开其余 ${group.members.length - 3} 个文件`}
        </button>
      )}
    </Card>
  );
}

function FileDetailDialog({ member, onClose }: { member: GroupMember | null; onClose: () => void }) {
  const detail = useQuery({
    queryKey: ["file", member?.file_id],
    queryFn: () => api.file(member!.file_id),
    enabled: !!member,
  });
  if (!member) return null;
  const d = detail.data;

  return (
    <Dialog open onClose={onClose} title={member.name} desc={member.path} width="max-w-2xl">
      <div className="space-y-3">
        <div className="overflow-hidden rounded-lg border border-border bg-black/85">
          <Preview fileId={member.file_id} kind={member.media_kind} className="max-h-[320px] w-full object-contain" />
        </div>

        {detail.isLoading ? (
          <Loading label="读取媒体信息…" className="py-4" />
        ) : (
          <div className="grid grid-cols-2 gap-x-6 gap-y-1.5 text-[12px] sm:grid-cols-3">
            <KV label="类型" value={kindLabel(member.media_kind)} />
            <KV label="大小" value={formatBytes(member.size)} />
            <KV label="分辨率" value={resolutionLabel(member.width, member.height)} />
            <KV label="时长" value={member.duration_ms ? formatClock(member.duration_ms) : "—"} />
            <KV label="码率" value={member.bitrate ? `${Math.round(member.bitrate / 1000)} kbps` : "—"} />
            <KV label="视频编码" value={member.video_codec || "—"} />
            <KV label="音频编码" value={member.audio_codec || "—"} />
            <KV label="存储卷" value={member.volume || "—"} />
            <KV label="修改时间" value={formatDate(member.mtime_ns / 1e6)} />
            <KV
              label="创建时间"
              value={member.btime_ns > 0 ? formatDate(member.btime_ns / 1e6) : "未知（该卷未提供出生时间）"}
            />
            <KV
              label="inode 变更"
              value={member.ctime_ns > 0 ? formatDate(member.ctime_ns / 1e6) : "—"}
            />
            {d ? (
              <>
                <KV label="采样指纹" value={d.quick_hash || "—"} mono />
                <KV label="内容 SHA" value={d.content_hash ? d.content_hash.slice(0, 16) : "—"} mono />
                <KV label="pHash" value={d.phash || "—"} mono />
              </>
            ) : null}
          </div>
        )}

        {member.keep_reason ? (
          <div className="rounded-md border border-success/25 bg-success/8 px-3 py-2 text-[12px] text-success">
            保留原因：{member.keep_reason}
          </div>
        ) : null}
      </div>
    </Dialog>
  );
}

function KV({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex min-w-0 items-baseline gap-1.5">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className={cn("truncate", mono && "font-mono text-[11px]")} title={value}>
        {value}
      </span>
    </div>
  );
}
