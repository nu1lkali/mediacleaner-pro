import { useMemo, useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
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
  ScanLine,
  Search,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import { api } from "../services/api";
import type { DupGroup, GroupKind, GroupMember } from "../services/types";
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
    desc: "内容逐字节一致的文件（已通过全文 SHA-256 校验），删除任意副本都不会损失信息。",
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
  const [detail, setDetail] = useState<GroupMember | null>(null);
  const [exportOpen, setExportOpen] = useState(false);

  const summary = useQuery({ queryKey: ["group-summary"], queryFn: api.groupSummary });
  const groups = useQuery({
    queryKey: ["groups", kind, page, sort, minReclaim],
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

  const clean = useMutation({
    mutationFn: () =>
      api.createCleanTask({
        name: `手动清理 · ${meta.title}`,
        file_ids: [...selected.keys()],
        execute_mode: "quarantine",
      }),
    onSuccess: () => {
      setSelected(new Map());
      setConfirmOpen(false);
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
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

  return (
    <div className="pb-10">
      <PageHeader
        title={meta.title}
        desc={meta.desc}
        actions={
          <>
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
                placeholder="搜索文件名/签名"
                className="h-8 w-full pl-8 text-[12px] sm:w-[190px]"
              />
            </div>
            <Select value={sort} onChange={(e) => { setSort(e.target.value); setPage(1); }} className="h-8 flex-1 text-[12px] sm:w-[130px] sm:flex-none">
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
        {/* 汇总条 */}
        <div className="grid gap-3 sm:grid-cols-3">
          <MiniStat icon={<Layers className="h-4 w-4" />} label="分组数" value={formatNumber(sumItem?.groups ?? 0)} />
          <MiniStat icon={<Copy className="h-4 w-4" />} label="可删副本" value={formatNumber(sumItem?.removable ?? 0)} />
          <MiniStat icon={<Sparkles className="h-4 w-4" />} label="可释放空间" value={formatBytes(sumItem?.bytes ?? 0)} />
        </div>

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
              />
            ))}
          </div>
        )}

        {totalPages > 1 && (
          <div className="flex items-center justify-between rounded-lg border border-border bg-card px-3 py-2">
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

      {/* 底部批量操作条 */}
      {selected.size > 0 && (
        <div className="fixed bottom-5 left-1/2 z-40 flex -translate-x-1/2 items-center gap-4 rounded-xl border border-border bg-card px-4 py-2.5 shadow-lift">
          <div className="text-[12.5px]">
            已选 <span className="font-semibold text-primary">{selected.size}</span> 个文件 ·{" "}
            <span className="tabular-nums">{formatBytes(selectedBytes)}</span>
          </div>
          <div className="flex items-center gap-1.5">
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
        open={confirmOpen}
        onClose={() => setConfirmOpen(false)}
        onConfirm={() => clean.mutate()}
        loading={clean.isPending}
        title="将选中文件移入隔离区？"
        confirmText="移入隔离区"
        danger
        desc={
          <>
            共 <b>{selected.size}</b> 个文件、<b>{formatBytes(selectedBytes)}</b>。文件会移动到隔离区而不是立即删除，
            可在保留期内随时恢复。每组的保留文件不会被移除。
            {clean.isError && (
              <div className="mt-2 text-destructive">{(clean.error as Error).message}</div>
            )}
          </>
        }
      />

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
}: {
  group: DupGroup;
  selected: Map<number, GroupMember>;
  onToggle: (m: GroupMember) => void;
  onSetKeeper: (fileId: number) => void;
  keeperBusy: boolean;
  onPreview: (m: GroupMember) => void;
}) {
  const [expanded, setExpanded] = useState(group.member_count <= 4);
  const shown = expanded ? group.members : group.members.slice(0, 3);
  const keeper = group.members.find((m) => m.is_keeper);

  return (
    <Card className="overflow-hidden">
      <CardHeader
        className="bg-muted/30 py-2.5"
        action={
          <div className="flex items-center gap-2">
            {group.kind !== "exact" && group.avg_similarity > 0 && (
              <Badge tone="outline">相似度 {(group.avg_similarity * 100).toFixed(1)}%</Badge>
            )}
            <Badge tone="info">{group.member_count} 个文件</Badge>
            <Badge tone="warn">可释放 {formatBytes(group.reclaimable_bytes)}</Badge>
          </div>
        }
      >
        <div className="flex items-center gap-2">
          <span className="mono text-[11px] text-muted-foreground">#{group.id}</span>
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
      </CardHeader>

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
                  {m.is_keeper && <Badge tone="success">保留</Badge>}
                  {m.similarity > 0 && m.similarity < 1 && (
                    <Badge tone="outline">{(m.similarity * 100).toFixed(0)}%</Badge>
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

              {!m.is_keeper && (
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
