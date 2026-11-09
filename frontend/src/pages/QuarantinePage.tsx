import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Clock,
  Eye,
  HardDriveDownload,
  RotateCcw,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { api } from "../services/api";
import type { QuarantineItem } from "../services/types";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Input, Select } from "../components/ui/field";
import { ConfirmDialog, Dialog } from "../components/ui/dialog";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui/empty";
import { cn, formatBytes, formatDate, formatNumber, kindLabel, statusText, statusTone } from "../lib/utils";

export default function QuarantinePage() {
  const qc = useQueryClient();
  const [status, setStatus] = useState("quarantined");
  const [keyword, setKeyword] = useState("");
  const [restoreTarget, setRestoreTarget] = useState<QuarantineItem | null>(null);
  const [purgeTarget, setPurgeTarget] = useState<QuarantineItem | null>(null);
  const [purgeExpiredOpen, setPurgeExpiredOpen] = useState(false);
  const [detailId, setDetailId] = useState<number | null>(null);

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
                    <th className="w-[130px]" />
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
