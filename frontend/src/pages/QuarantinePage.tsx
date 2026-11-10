import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Clock,
  Crosshair,
  Eye,
  HardDriveDownload,
  Play,
  RotateCcw,
  Search,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { api } from "../services/api";
import type { QuarantineItem, QuarantineOriginMatch } from "../services/types";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Input, Select } from "../components/ui/field";
import { ConfirmDialog, Dialog } from "../components/ui/dialog";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui/empty";
import { Preview, Thumb } from "../components/Thumb";
import { cn, formatBytes, formatDate, formatNumber, kindLabel, statusText, statusTone } from "../lib/utils";

export default function QuarantinePage() {
  const qc = useQueryClient();
  const [status, setStatus] = useState("quarantined");
  const [keyword, setKeyword] = useState("");
  const [restoreTarget, setRestoreTarget] = useState<QuarantineItem | null>(null);
  const [purgeTarget, setPurgeTarget] = useState<QuarantineItem | null>(null);
  const [purgeExpiredOpen, setPurgeExpiredOpen] = useState(false);
  const [detailId, setDetailId] = useState<number | null>(null);
  // 主本反查：按隔离记录 id 或直接粘贴隔离文件名的哈希查保留副本，不需要重新扫描
  const [originId, setOriginId] = useState<number | null>(null);
  const [originHash, setOriginHash] = useState<string | null>(null);
  const [hashInput, setHashInput] = useState("");
  const [preview, setPreview] = useState<
    { fileId?: number; quarantineId?: number; kind: string; name: string } | null
  >(null);

  const q = useQuery({
    queryKey: ["quarantine", status],
    queryFn: () => api.quarantine(status, 100, 0),
    refetchInterval: 20000,
  });

  const detail = useQuery({
    queryKey: ["quarantine-item", detailId],
    queryFn: () => api.quarantineItem(detailId as number),
    enabled: detailId != null,
  });

  const origin = useQuery({
    queryKey: ["quarantine-origin", originId, originHash],
    queryFn: () =>
      originId != null ? api.quarantineOrigin(originId) : api.quarantineOriginByHash(originHash as string),
    enabled: originId != null || originHash != null,
  });

  const originOpen = originId != null || originHash != null;
  const closeOrigin = () => {
    setOriginId(null);
    setOriginHash(null);
  };
  // 粘贴完整隔离文件名也行：剥掉扩展名后提交
  const submitHash = () => {
    const h = hashInput.trim().replace(/\.[A-Za-z0-9]{1,8}$/, "");
    if (!h) return;
    setOriginId(null);
    setOriginHash(h);
  };

  const restore = useMutation({
    mutationFn: (id: number) => api.restore(id),
    onSuccess: () => {
      setRestoreTarget(null);
      qc.invalidateQueries({ queryKey: ["quarantine"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });

  const purge = useMutation({
    mutationFn: (id: number) => api.purge(id),
    onSuccess: () => {
      setPurgeTarget(null);
      qc.invalidateQueries({ queryKey: ["quarantine"] });
      qc.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });

  const purgeExpired = useMutation({
    mutationFn: () => api.purgeExpired(),
    onSuccess: () => {
      setPurgeExpiredOpen(false);
      qc.invalidateQueries({ queryKey: ["quarantine"] });
    },
  });

  const items = (q.data?.items ?? []).filter((it) =>
    keyword.trim() ? it.orig_path.toLowerCase().includes(keyword.trim().toLowerCase()) : true,
  );

  return (
    <div className="pb-10">
      <PageHeader
        title="隔离区"
        desc="被清理的文件先移动到这里，保留期内可原样恢复。过期后可由后台任务自动永久删除。"
        actions={
          <>
            <div className="relative">
              <Input
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
                placeholder="搜索原始路径"
                className="h-8 w-full text-[12px] sm:w-[190px]"
              />
            </div>
            <div className="flex w-full items-center gap-1 sm:w-auto">
              <Input
                value={hashInput}
                onChange={(e) => setHashInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") submitHash();
                }}
                placeholder="粘贴哈希反查主本，如 fc5d2032…"
                className="h-8 w-full text-[12px] sm:w-[220px]"
                spellCheck={false}
              />
              <Button
                size="sm"
                variant="outline"
                className="h-8 shrink-0"
                icon={<Search className="h-3.5 w-3.5" />}
                onClick={submitHash}
                disabled={!hashInput.trim()}
              >
                反查
              </Button>
            </div>
            <Select value={status} onChange={(e) => setStatus(e.target.value)} className="h-8 flex-1 text-[12px] sm:w-[130px] sm:flex-none">
              <option value="quarantined">待处理</option>
              <option value="restored">已恢复</option>
              <option value="purged">已永久删除</option>
              <option value="all">全部</option>
            </Select>
            <Button
              size="sm"
              variant="outline"
              icon={<Trash2 className="h-3.5 w-3.5" />}
              onClick={() => setPurgeExpiredOpen(true)}
            >
              清理过期
            </Button>
          </>
        }
      />

      <div className="space-y-3 px-4 sm:px-5">
        <div className="grid gap-3 sm:grid-cols-3">
          <InfoCard
            icon={<HardDriveDownload className="h-4 w-4" />}
            label="隔离占用"
            value={formatBytes(q.data?.quarantined_bytes ?? 0)}
            hint={`${formatNumber(q.data?.quarantined_count ?? 0)} 个文件待确认`}
          />
          <InfoCard
            icon={<Clock className="h-4 w-4" />}
            label="保留期"
            value={`${q.data?.retention_days ?? 0} 天`}
            hint="过期后由后台任务自动清除"
          />
          <InfoCard
            icon={<ShieldCheck className="h-4 w-4" />}
            label="可恢复性"
            value="原路径还原"
            hint="恢复时保留原始目录结构"
          />
        </div>

        {q.isLoading ? (
          <Loading />
        ) : items.length === 0 ? (
          <Card>
            <EmptyState
              icon={<ShieldCheck className="h-5 w-5" />}
              title="隔离区是空的"
              desc="清理任务执行后，被移除的文件会出现在这里。"
            />
          </Card>
        ) : (
          <Card>
            <CardHeader action={<span className="text-[11.5px] text-muted-foreground">共 {formatNumber(q.data?.total ?? 0)} 条</span>}>
              <CardTitle hint="按隔离时间倒序">文件列表</CardTitle>
            </CardHeader>
            <div className="overflow-x-auto">
              <table className="table">
                <thead>
                  <tr>
                    <th>原始路径</th>
                    <th className="w-[64px]">类型</th>
                    <th className="w-[84px]">大小</th>
                    <th className="w-[96px]">状态</th>
                    <th className="w-[126px]">隔离时间</th>
                    <th className="w-[126px]">到期时间</th>
                    <th className="w-[150px]" />
                  </tr>
                </thead>
                <tbody>
                  {items.map((it) => {
                    const expired = it.expires_at > 0 && it.expires_at < Date.now();
                    return (
                      <tr key={it.id} className={cn(expired && it.status === "quarantined" && "bg-warning/[0.05]")}>
                        <td className="max-w-[380px]">
                          <div className="truncate text-[12px]" title={it.orig_path}>
                            {it.orig_path.split(/[\\/]/).pop()}
                          </div>
                          <div className="truncate text-[10.5px] text-muted-foreground" title={it.orig_path}>
                            {it.orig_path}
                          </div>
                        </td>
                        <td className="text-[11.5px]">{kindLabel(it.media_kind)}</td>
                        <td className="text-[11.5px] tabular-nums">{formatBytes(it.size)}</td>
                        <td>
                          <Badge tone={statusTone(it.status)}>{statusText[it.status] ?? it.status}</Badge>
                        </td>
                        <td className="text-[11.5px] tabular-nums text-muted-foreground">{formatDate(it.quarantined_at)}</td>
                        <td className={cn("text-[11.5px] tabular-nums", expired ? "text-warning" : "text-muted-foreground")}>
                          {formatDate(it.expires_at)}
                          {expired && it.status === "quarantined" ? " · 已过期" : ""}
                        </td>
                        <td>
                          <div className="flex items-center justify-end gap-1">
                            <Button
                              size="sm"
                              variant="ghost"
                              icon={<Eye className="h-3.5 w-3.5" />}
                              onClick={() => setDetailId(it.id)}
                            />
                            <Button
                              size="sm"
                              variant="ghost"
                              title="反查主本（仍在库里的同内容文件）"
                              icon={<Crosshair className="h-3.5 w-3.5" />}
                              onClick={() => {
                                setOriginHash(null);
                                setOriginId(it.id);
                              }}
                            >
                              主本
                            </Button>
                            {it.status === "quarantined" && (
                              <Button
                                size="sm"
                                variant="ghost"
                                title="预览隔离区里的这份文件"
                                icon={<Play className="h-3.5 w-3.5" />}
                                onClick={() =>
                                  setPreview({
                                    quarantineId: it.id,
                                    kind: it.media_kind,
                                    name: it.orig_path.split(/[\\/]/).pop() ?? it.orig_path,
                                  })
                                }
                              />
                            )}
                            {it.status === "quarantined" && (
                              <>
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  icon={<RotateCcw className="h-3.5 w-3.5" />}
                                  onClick={() => setRestoreTarget(it)}
                                >
                                  恢复
                                </Button>
                                <Button
                                  size="sm"
                                  variant="ghost"
                                  className="text-destructive hover:bg-destructive/10"
                                  icon={<Trash2 className="h-3.5 w-3.5" />}
                                  onClick={() => setPurgeTarget(it)}
                                />
                              </>
                            )}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </Card>
        )}

        <div className="flex items-start gap-2 rounded-md border border-border bg-muted/40 px-3 py-2 text-[11.5px] text-muted-foreground">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" />
          恢复时如果原路径已有同名文件，系统不会覆盖，而会在文件名后追加时间戳，恢复结果会显示在提示中。
        </div>
      </div>

      <Dialog
        open={detailId != null}
        onClose={() => setDetailId(null)}
        title="隔离记录详情"
        desc={detail.data ? `#${detail.data.id}` : undefined}
        width="max-w-lg"
        footer={
          <>
            <Button size="sm" variant="ghost" onClick={() => setDetailId(null)}>
              关闭
            </Button>
            {detail.data?.status === "quarantined" && (
              <>
                <Button
                  size="sm"
                  variant="outline"
                  icon={<RotateCcw className="h-3.5 w-3.5" />}
                  onClick={() => {
                    const item = detail.data;
                    setDetailId(null);
                    if (item) setRestoreTarget(item);
                  }}
                >
                  恢复
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  icon={<Trash2 className="h-3.5 w-3.5" />}
                  onClick={() => {
                    const item = detail.data;
                    setDetailId(null);
                    if (item) setPurgeTarget(item);
                  }}
                >
                  永久删除
                </Button>
              </>
            )}
          </>
        }
      >
        {detail.isLoading ? (
          <Loading label="读取记录…" className="py-6" />
        ) : detail.error ? (
          <ErrorBox message={(detail.error as Error).message} />
        ) : detail.data ? (
          <div className="space-y-2 text-[12px]">
            <DetailRow label="原始路径" value={detail.data.orig_path} mono breakAll />
            <DetailRow label="隔离区路径" value={detail.data.stored_path || "—"} mono breakAll />
            <DetailRow label="大小" value={formatBytes(detail.data.size)} />
            <DetailRow label="类型" value={kindLabel(detail.data.media_kind)} />
            <DetailRow
              label="状态"
              value={<Badge tone={statusTone(detail.data.status)}>{statusText[detail.data.status] ?? detail.data.status}</Badge>}
            />
            <DetailRow label="SHA-256" value={detail.data.sha256 || "—"} mono breakAll />
            <DetailRow label="规则" value={detail.data.rule_id ? `#${detail.data.rule_id}` : "手动清理"} />
            <DetailRow label="清理任务" value={detail.data.clean_task_id ? `#${detail.data.clean_task_id}` : "—"} />
            <DetailRow label="隔离时间" value={formatDate(detail.data.quarantined_at)} />
            <DetailRow label="到期时间" value={formatDate(detail.data.expires_at)} />
            {detail.data.restored_at ? (
              <DetailRow label="恢复时间" value={formatDate(detail.data.restored_at)} />
            ) : null}
            {detail.data.message ? <DetailRow label="备注" value={detail.data.message} /> : null}
          </div>
        ) : null}
      </Dialog>

      <Dialog
        open={originOpen}
        onClose={closeOrigin}
        title="主本反查"
        desc={
          origin.data
            ? originHash != null
              ? `按哈希 ${originHash.slice(0, 16)}… 定位`
              : `隔离记录 #${origin.data.quarantine_id}`
            : undefined
        }
        width="max-w-lg"
        footer={
          <Button size="sm" variant="ghost" onClick={closeOrigin}>
            关闭
          </Button>
        }
      >
        {origin.isLoading ? (
          <Loading label="正在反查…" className="py-6" />
        ) : origin.error ? (
          <ErrorBox message={(origin.error as Error).message} />
        ) : origin.data ? (
          <div className="space-y-3 text-[12px]">
            <DetailRow
              label="隔离文件"
              value={
                <span className="flex min-w-0 items-center gap-1">
                  <span className="truncate">
                    {origin.data.orig_path.split(/[\\/]/).pop() ?? origin.data.orig_path}
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-6 shrink-0 px-1.5"
                    title="预览这份隔离文件"
                    icon={<Eye className="h-3.5 w-3.5" />}
                    onClick={() => {
                      const od = origin.data;
                      if (!od) return;
                      setPreview({
                        quarantineId: od.quarantine_id,
                        kind: od.media_kind,
                        name: od.orig_path.split(/[\\/]/).pop() ?? od.orig_path,
                      });
                    }}
                  />
                </span>
              }
            />
            <DetailRow label="定位方式" value={methodLabel(origin.data.method)} />
            <div className="rounded-md border border-border bg-muted/40 px-3 py-2 text-[11.5px] text-muted-foreground">
              {origin.data.note}
            </div>

            {origin.data.matches.length === 0 ? (
              <EmptyState
                icon={<ShieldCheck className="h-5 w-5" />}
                title="没有找到主本"
                desc="索引里已没有与它同内容、且仍在库里的文件。"
                className="py-6"
              />
            ) : (
              <div className="space-y-2">
                {origin.data.matches.map((m) => (
                  <OriginMatchCard key={m.file_id} m={m} onPreview={() => setPreview({ fileId: m.file_id, kind: m.media_kind, name: m.name })} />
                ))}
              </div>
            )}
          </div>
        ) : null}
      </Dialog>

      <Dialog
        open={preview != null}
        onClose={() => setPreview(null)}
        title={preview?.name ?? "预览"}
        desc={
          preview?.quarantineId != null
            ? "隔离区里的这份文件"
            : origin.data?.matches.find((m) => m.file_id === preview?.fileId)?.dir
        }
        width="max-w-2xl"
        footer={
          <Button size="sm" variant="ghost" onClick={() => setPreview(null)}>
            关闭
          </Button>
        }
      >
        <div className="overflow-hidden rounded-lg border border-border bg-black/85">
          <Preview
            fileId={preview?.fileId}
            quarantineId={preview?.quarantineId}
            kind={preview?.kind ?? "other"}
            className="max-h-[320px] w-full object-contain"
          />
        </div>
      </Dialog>

      <ConfirmDialog
        open={!!restoreTarget}
        onClose={() => setRestoreTarget(null)}
        onConfirm={() => restoreTarget && restore.mutate(restoreTarget.id)}
        loading={restore.isPending}
        title="恢复这个文件？"
        confirmText="恢复"
        desc={
          <>
            将 <b>{restoreTarget?.orig_path.split(/[\\/]/).pop()}</b> 恢复到原路径：
            <div className="mt-1 break-all font-mono text-[11px]">{restoreTarget?.orig_path}</div>
          </>
        }
      />

      <ConfirmDialog
        open={!!purgeTarget}
        onClose={() => setPurgeTarget(null)}
        onConfirm={() => purgeTarget && purge.mutate(purgeTarget.id)}
        loading={purge.isPending}
        danger
        title="永久删除这个文件？"
        confirmText="永久删除"
        desc={
          <>
            这会从磁盘上彻底移除 <b>{formatBytes(purgeTarget?.size ?? 0)}</b>，不可恢复。
          </>
        }
      />

      <ConfirmDialog
        open={purgeExpiredOpen}
        onClose={() => setPurgeExpiredOpen(false)}
        onConfirm={() => purgeExpired.mutate()}
        loading={purgeExpired.isPending}
        danger
        title="清理所有过期隔离文件？"
        confirmText="清理过期"
        desc="所有超过保留期的隔离文件将被永久删除。未过期的文件不受影响。"
      />
    </div>
  );
}

function methodLabel(method: string): string {
  return (
    {
      group: "分组关系（隔离时判定保留的那份）",
      hash: "全文哈希匹配（分组已重建）",
      none: "未能定位",
    } as Record<string, string>
  )[method] ?? method;
}

function OriginMatchCard({ m, onPreview }: { m: QuarantineOriginMatch; onPreview: () => void }) {
  const active = m.state === "active";
  return (
    <div className="flex items-center gap-3 rounded-md border border-border px-3 py-2">
      <div className="h-12 w-16 shrink-0 overflow-hidden rounded bg-muted">
        <Thumb fileId={m.file_id} kind={m.media_kind} width={160} className="h-full w-full" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          {m.is_keeper && <Badge tone="success">保留（主本）</Badge>}
          {!active && <Badge tone="warn">{statusText[m.state] ?? m.state}</Badge>}
          <span className="truncate text-[12px] font-medium" title={m.name}>
            {m.name}
          </span>
        </div>
        <div className="mt-0.5 truncate text-[10.5px] text-muted-foreground" title={m.path}>
          {m.dir || m.path}
        </div>
        <div className="mt-0.5 text-[10.5px] text-muted-foreground">
          {kindLabel(m.media_kind)} · {formatBytes(m.size)} · 修改于 {formatDate(m.mtime_ns / 1e6)}
        </div>
      </div>
      {active && (
        <Button size="sm" variant="ghost" icon={<Eye className="h-3.5 w-3.5" />} onClick={onPreview}>
          预览
        </Button>
      )}
    </div>
  );
}

function DetailRow({
  label,
  value,
  mono,
  breakAll,
}: {
  label: string;
  value: React.ReactNode;
  mono?: boolean;
  breakAll?: boolean;
}) {
  return (
    <div className="flex gap-3 border-b border-border/60 py-1.5 last:border-0">
      <span className="w-[86px] shrink-0 text-muted-foreground">{label}</span>
      <span className={cn("min-w-0 flex-1", mono && "font-mono text-[11px]", breakAll && "break-all")}>{value}</span>
    </div>
  );
}

function InfoCard({
  icon,
  label,
  value,
  hint,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  hint: string;
}) {
  return (
    <Card className="flex items-center gap-3 px-4 py-3">
      <div className="rounded-md bg-primary/10 p-2 text-primary">{icon}</div>
      <div className="min-w-0">
        <div className="text-[11px] text-muted-foreground">{label}</div>
        <div className="text-[15px] font-semibold tabular-nums">{value}</div>
        <div className="truncate text-[10.5px] text-muted-foreground">{hint}</div>
      </div>
    </Card>
  );
}
