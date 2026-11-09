import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  Download,
  FileWarning,
  History,
  RefreshCw,
  ScanLine,
  Trash2,
} from "lucide-react";
import { api } from "../services/api";
import { Badge, Card, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Dialog } from "../components/ui/dialog";
import { EmptyState, Loading, PageHeader } from "../components/ui/empty";
import { Progress } from "../components/ui/progress";
import { cn, formatBytes, formatDate, formatNumber, groupKindLabel, phaseText, statusText, statusTone } from "../lib/utils";

export default function HistoryPage() {
  const scans = useQuery({ queryKey: ["scans-all"], queryFn: () => api.scans(200, 0) });
  const [detailId, setDetailId] = useState<number | null>(null);
  const [exportOpen, setExportOpen] = useState(false);

  const list = scans.data?.items ?? [];
  const detail = list.find((t) => t.id === detailId) ?? null;

  return (
    <div className="pb-10">
      <PageHeader
        title="扫描历史"
        desc="每次扫描的完整记录：耗时、处理量、错误明细。删除扫描记录不会影响已发现的重复分组。"
        actions={
          <Button
            size="sm"
            variant="ghost"
            icon={<RefreshCw className="h-3.5 w-3.5" />}
            onClick={() => scans.refetch()}
          >
            刷新
          </Button>
        }
      />

      <div className="px-4 sm:px-5">
        {scans.isLoading ? (
          <Loading />
        ) : list.length === 0 ? (
          <Card>
            <EmptyState icon={<History className="h-5 w-5" />} title="还没有扫描记录" desc="到「媒体扫描」页面发起第一次扫描。" />
          </Card>
        ) : (
          <Card>
            <CardHeader action={<span className="text-[11.5px] text-muted-foreground">共 {formatNumber(list.length)} 条</span>}>
              <CardTitle hint="点击行查看详情">全部扫描</CardTitle>
            </CardHeader>
            <div className="overflow-x-auto">
              <table className="table">
                <thead>
                  <tr>
                    <th className="w-[60px]">ID</th>
                    <th>名称</th>
                    <th className="w-[120px]">能力</th>
                    <th className="w-[96px]">状态</th>
                    <th className="w-[92px]">文件</th>
                    <th className="w-[92px]">数据量</th>
                    <th className="w-[70px]">错误</th>
                    <th className="w-[126px]">开始</th>
                    <th className="w-[126px]">结束</th>
                    <th className="w-[56px]" />
                  </tr>
                </thead>
                <tbody>
                  {list.map((t) => (
                    <tr
                      key={t.id}
                      onClick={() => setDetailId(t.id)}
                      className={cn("cursor-pointer hover:bg-muted/50", detailId === t.id && "bg-primary/[0.04]")}
                    >
                      <td className="mono text-muted-foreground">#{t.id}</td>
                      <td className="max-w-[240px] truncate">{t.name}</td>
                      <td>
                        <div className="flex gap-1">
                          {t.options.detect_exact && <Badge tone="outline">重复</Badge>}
                          {t.options.detect_similar_image && <Badge tone="outline">图</Badge>}
                          {t.options.detect_similar_video && <Badge tone="outline">视频</Badge>}
                        </div>
                      </td>
                      <td>
                        <Badge tone={statusTone(t.status)}>{statusText[t.status] ?? t.status}</Badge>
                      </td>
                      <td className="tabular-nums">{formatNumber(t.files_seen)}</td>
                      <td className="tabular-nums text-muted-foreground">{formatBytes(t.bytes_scanned)}</td>
                      <td className={cn("tabular-nums", t.error_count > 0 && "text-warning")}>{t.error_count}</td>
                      <td className="text-[11.5px] tabular-nums text-muted-foreground">{formatDate(t.started_at)}</td>
                      <td className="text-[11.5px] tabular-nums text-muted-foreground">{formatDate(t.finished_at)}</td>
                      <td>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="text-destructive hover:bg-destructive/10"
                          icon={<Trash2 className="h-3.5 w-3.5" />}
                          onClick={async (e) => {
                            e.stopPropagation();
                            await api.deleteScan(t.id);
                            if (detailId === t.id) setDetailId(null);
                            scans.refetch();
                          }}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}
      </div>

      {detail && (
        <Dialog
          open
          onClose={() => setDetailId(null)}
          width="max-w-2xl"
          title={detail.name}
          desc={`#${detail.id} · ${detail.mode === "full" ? "全量" : "增量"}扫描 · ${formatDate(detail.queued_at)}`}
          footer={
            <>
              <Button size="sm" variant="ghost" icon={<Download className="h-3.5 w-3.5" />} onClick={() => setExportOpen(true)}>
                导出分组
              </Button>
              <Button size="sm" variant="outline" onClick={() => setDetailId(null)}>
                关闭
              </Button>
            </>
          }
        >
          <ScanDetail id={detail.id} />
        </Dialog>
      )}

      {exportOpen && <ExportGroupsDialog onClose={() => setExportOpen(false)} />}
    </div>
  );
}

function ScanDetail({ id }: { id: number }) {
  const task = useQuery({ queryKey: ["scan", id], queryFn: () => api.scan(id), refetchInterval: 3000 });
  const errors = useQuery({ queryKey: ["scan-errors", id], queryFn: () => api.scanErrors(id, 100) });

  if (task.isLoading) return <Loading />;
  const t = task.data;
  if (!t) return null;
  const percent = typeof t.progress?.percent === "number" ? t.progress.percent : 0;
  const elapsed =
    t.started_at && t.finished_at ? Math.round((t.finished_at - t.started_at) / 1000) : 0;

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Cell label="状态" value={statusText[t.status] ?? t.status} />
        <Cell label="阶段" value={phaseText[t.phase] ?? t.phase} />
        <Cell label="已扫描" value={formatNumber(t.files_seen)} />
        <Cell label="已索引" value={formatNumber(t.files_indexed)} />
        <Cell label="数据量" value={formatBytes(t.bytes_scanned)} />
        <Cell label="耗时" value={elapsed ? `${elapsed}s` : "—"} />
        <Cell label="错误" value={String(t.error_count)} warn={t.error_count > 0} />
        <Cell label="优先级" value={String(t.priority)} />
      </div>

      {percent > 0 && <Progress value={percent} />}
      {t.message && <div className="text-[12px] text-muted-foreground">{t.message}</div>}

      <div className="rounded-md border border-border">
        <div className="border-b border-border bg-muted/50 px-3 py-1.5 text-[11.5px] font-medium text-muted-foreground">
          扫描配置
        </div>
        <div className="grid gap-x-4 gap-y-1 px-3 py-2 text-[11.5px] sm:grid-cols-2">
          <KV k="预设" v={t.options.profile || "—"} />
          <KV k="完全重复" v={t.options.detect_exact ? "开" : "关"} />
          <KV k="相似图片" v={t.options.detect_similar_image ? "开" : "关"} />
          <KV k="相似视频" v={t.options.detect_similar_video ? "开" : "关"} />
          <KV k="最小体积" v={t.options.min_file_size ? formatBytes(t.options.min_file_size) : "不限"} />
          <KV k="最大深度" v={t.options.max_depth ? String(t.options.max_depth) : "不限"} />
          <KV k="排除目录" v={t.options.exclude_dirs?.join("、") || "无"} />
          <KV k="线程/限速" v={`${t.options.threads || "自动"} / ${t.options.limit_mbps ? `${t.options.limit_mbps}MB/s` : "不限"}`} />
        </div>
      </div>

      <div className="rounded-md border border-border">
        <div className="flex items-center justify-between border-b border-border bg-muted/50 px-3 py-1.5">
          <span className="text-[11.5px] font-medium text-muted-foreground">
            错误明细 {errors.data?.items.length ? `(${errors.data.items.length})` : ""}
          </span>
        </div>
        {errors.isLoading ? (
          <Loading label="读取错误日志…" className="py-6" />
        ) : !errors.data?.items.length ? (
          <div className="flex items-center gap-2 px-3 py-4 text-[12px] text-muted-foreground">
            <FileWarning className="h-3.5 w-3.5" />
            没有错误记录
          </div>
        ) : (
          <div className="max-h-[220px] overflow-y-auto">
            {errors.data.items.map((e, i) => (
              <div key={i} className="flex items-start gap-2 border-b border-border/60 px-3 py-1.5 last:border-0">
                <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-warning" />
                <div className="min-w-0">
                  <div className="truncate font-mono text-[10.5px]" title={e.path}>
                    {e.path || "(未知路径)"}
                  </div>
                  <div className="text-[11px] text-muted-foreground">
                    {e.code} · {e.message}
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function ExportGroupsDialog({ onClose }: { onClose: () => void }) {
  const [kind, setKind] = useState<string>("all");
  const [format, setFormat] = useState<"csv" | "xlsx" | "json" | "html">("xlsx");
  const [result, setResult] = useState<{ file: string; rows: number; download_url: string } | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const run = async () => {
    setLoading(true);
    setError("");
    try {
      const r = await api.exportData({
        kind: "groups",
        format,
        kind_filter: kind === "all" ? undefined : kind,
      });
      setResult(r);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title="导出重复分组"
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
            <Button size="sm" variant="primary" loading={loading} onClick={run}>
              生成文件
            </Button>
          )}
        </>
      }
    >
      <div className="space-y-3">
        <div>
          <div className="label">分组类型</div>
          <div className="flex flex-wrap gap-1.5">
            {[
              ["all", "全部"],
              ["exact", groupKindLabel("exact")],
              ["image", groupKindLabel("image")],
              ["video", groupKindLabel("video")],
            ].map(([k, l]) => (
              <button
                key={k}
                onClick={() => setKind(k)}
                className={cn(
                  "rounded-md border px-2 py-1 text-[12px]",
                  kind === k ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:bg-muted",
                )}
              >
                {l}
              </button>
            ))}
          </div>
        </div>
        <div>
          <div className="label">格式</div>
          <div className="flex flex-wrap gap-1.5">
            {(["xlsx", "csv", "json", "html"] as const).map((f) => (
              <button
                key={f}
                onClick={() => setFormat(f)}
                className={cn(
                  "rounded-md border px-2.5 py-1 text-[12px] uppercase",
                  format === f ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:bg-muted",
                )}
              >
                {f}
              </button>
            ))}
          </div>
        </div>
        {error && <div className="text-[12px] text-destructive">{error}</div>}
        {result && (
          <div className="rounded-md border border-success/30 bg-success/8 px-3 py-2 text-[12px] text-success">
            已生成 {result.rows} 行 · {result.file}
          </div>
        )}
      </div>
    </Dialog>
  );
}

function Cell({ label, value, warn }: { label: string; value: string; warn?: boolean }) {
  return (
    <div className="rounded-md border border-border px-3 py-2">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className={cn("mt-0.5 text-[13px] font-medium tabular-nums", warn && "text-warning")}>{value}</div>
    </div>
  );
}

function KV({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span className="shrink-0 text-muted-foreground">{k}</span>
      <span className="truncate" title={v}>
        {v}
      </span>
    </div>
  );
}
