import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, BarChart3, CheckCircle2, FolderOpen, HardDrive, Info, Sparkles } from "lucide-react";
import { api } from "../services/api";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui/empty";
import { Progress } from "../components/ui/progress";
import { cn, formatBytes, formatNumber, kindLabel, percent } from "../lib/utils";

export default function StatsPage() {
  const lib = useQuery({ queryKey: ["library-stats"], queryFn: api.libraryStats });
  const tips = useQuery({ queryKey: ["suggestions"], queryFn: api.suggestions });
  const dash = useQuery({ queryKey: ["dashboard"], queryFn: api.dashboard });

  if (lib.isLoading) return <Loading />;
  if (lib.error) return <ErrorBox message={(lib.error as Error).message} onRetry={() => lib.refetch()} />;
  const d = lib.data!;
  const totalBytes = d.by_ext.reduce((s, x) => s + x.bytes, 0);

  return (
    <div className="pb-10">
      <PageHeader title="数据统计" desc="按扩展名、分辨率、存储卷拆解媒体库构成，并给出可执行的优化建议。" />

      <div className="space-y-4 px-4 sm:px-5">
        {dash.data && (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            <Tile label="索引文件" value={formatNumber(dash.data.files.total)} sub={formatBytes(dash.data.files.bytes)} />
            <Tile label="重复分组" value={formatNumber(dash.data.duplicates.exact_groups + dash.data.duplicates.image_groups + dash.data.duplicates.video_groups)} sub="完全重复 + 相似" />
            <Tile label="可回收" value={formatBytes(dash.data.duplicates.reclaimable)} sub="按每组保留 1 份计算" tone="success" />
            <Tile label="已清理" value={formatBytes(dash.data.cleaned.bytes)} sub={`${formatNumber(dash.data.cleaned.files)} 个文件`} tone="primary" />
          </div>
        )}

        <div className="grid gap-4 xl:grid-cols-2">
          <Card>
            <CardHeader action={<span className="text-[11.5px] text-muted-foreground">按体积排序</span>}>
              <CardTitle hint="前 15 种扩展名">扩展名分布</CardTitle>
            </CardHeader>
            <CardBody className="space-y-2">
              {d.by_ext.length === 0 ? (
                <EmptyState icon={<BarChart3 className="h-5 w-5" />} title="暂无数据" className="py-8" />
              ) : (
                d.by_ext.slice(0, 15).map((x) => (
                  <div key={x.ext}>
                    <div className="flex items-baseline justify-between text-[12px]">
                      <span className="font-mono text-[11.5px]">{x.ext || "(无扩展名)"}</span>
                      <span className="tabular-nums text-muted-foreground">
                        {formatNumber(x.files)} 个 · {formatBytes(x.bytes)}
                      </span>
                    </div>
                    <Progress value={percent(x.bytes, totalBytes)} height={4} className="mt-1" />
                  </div>
                ))
              )}
            </CardBody>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle hint="按文件数排序">分辨率分布</CardTitle>
            </CardHeader>
            <CardBody className="space-y-2">
              {d.by_resolution.length === 0 ? (
                <EmptyState icon={<BarChart3 className="h-5 w-5" />} title="暂无数据" desc="需要扫描并读取过媒体元数据。" className="py-8" />
              ) : (
                d.by_resolution.slice(0, 12).map((x) => (
                  <div key={x.bucket}>
                    <div className="flex items-baseline justify-between text-[12px]">
                      <span>{x.bucket}</span>
                      <span className="tabular-nums text-muted-foreground">
                        {formatNumber(x.files)} 个 · {formatBytes(x.bytes)}
                      </span>
                    </div>
                    <Progress
                      value={percent(x.bytes, d.by_resolution.reduce((s, y) => s + y.bytes, 0))}
                      height={4}
                      className="mt-1"
                      tone="success"
                    />
                  </div>
                ))
              )}
            </CardBody>
          </Card>
        </div>

        <div className="grid gap-4 xl:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle hint="按存储卷">卷占用</CardTitle>
            </CardHeader>
            <CardBody className="space-y-2.5">
              {d.by_root.length === 0 ? (
                <EmptyState icon={<HardDrive className="h-5 w-5" />} title="暂无存储卷数据" className="py-8" />
              ) : (
                d.by_root.map((r) => (
                  <div key={r.name}>
                    <div className="flex items-baseline justify-between text-[12px]">
                      <span className="flex items-center gap-1.5">
                        <HardDrive className="h-3 w-3 text-muted-foreground" />
                        {r.name}
                        {r.volume ? <Badge tone="outline">{r.volume}</Badge> : null}
                      </span>
                      <span className="tabular-nums text-muted-foreground">
                        {formatNumber(r.files)} 个 · {formatBytes(r.bytes)}
                      </span>
                    </div>
                    <Progress value={percent(r.bytes, d.by_root.reduce((s, y) => s + y.bytes, 0))} height={4} className="mt-1" />
                  </div>
                ))
              )}
            </CardBody>
          </Card>

          <Card>
            <CardHeader action={<Sparkles className="h-3.5 w-3.5 text-primary" />}>
              <CardTitle hint="基于当前索引自动生成">优化建议</CardTitle>
            </CardHeader>
            {tips.isLoading ? (
              <Loading label="分析中…" className="py-8" />
            ) : !tips.data?.items.length ? (
              <EmptyState icon={<CheckCircle2 className="h-5 w-5" />} title="没有发现问题" desc="媒体库状态良好。" className="py-8" />
            ) : (
              <div className="divide-y divide-border/70">
                {tips.data.items.map((s) => (
                  <div key={s.key} className="flex items-start gap-2.5 px-4 py-2.5">
                    <span
                      className={cn(
                        "mt-0.5",
                        s.severity === "warn" ? "text-warning" : s.severity === "ok" ? "text-success" : "text-primary",
                      )}
                    >
                      {s.severity === "warn" ? (
                        <AlertTriangle className="h-3.5 w-3.5" />
                      ) : s.severity === "ok" ? (
                        <CheckCircle2 className="h-3.5 w-3.5" />
                      ) : (
                        <Info className="h-3.5 w-3.5" />
                      )}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="text-[12.5px] font-medium">{s.title}</div>
                      <div className="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">{s.detail}</div>
                    </div>
                    <div className="shrink-0 text-right">
                      <div className="text-[11.5px] font-medium tabular-nums">{formatBytes(s.bytes)}</div>
                      <div className="text-[10.5px] text-muted-foreground">{formatNumber(s.files)} 个</div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Card>
        </div>

        {tips.data?.top_dirs?.length ? (
          <Card>
            <CardHeader>
              <CardTitle hint="占用最多的目录，适合优先整理">体积最大的目录</CardTitle>
            </CardHeader>
            <div className="overflow-x-auto">
              <table className="table">
                <thead>
                  <tr>
                    <th>目录</th>
                    <th className="w-[110px]">文件数</th>
                    <th className="w-[110px]">占用</th>
                    <th className="w-[140px]">占比</th>
                  </tr>
                </thead>
                <tbody>
                  {tips.data.top_dirs.map((x) => (
                    <tr key={x.dir}>
                      <td className="max-w-[460px]">
                        <div className="flex items-center gap-1.5">
                          <FolderOpen className="h-3 w-3 shrink-0 text-muted-foreground" />
                          <span className="truncate font-mono text-[11px]" title={x.dir}>
                            {x.dir}
                          </span>
                        </div>
                      </td>
                      <td className="tabular-nums">{formatNumber(x.files)}</td>
                      <td className="tabular-nums">{formatBytes(x.bytes)}</td>
                      <td>
                        <div className="flex items-center gap-2">
                          <Progress value={percent(x.bytes, tips.data.top_dirs[0].bytes)} height={4} className="w-20" />
                          <span className="text-[11px] tabular-nums text-muted-foreground">
                            {percent(x.bytes, totalBytes).toFixed(1)}%
                          </span>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        ) : null}

        {dash.data && (
          <Card>
            <CardHeader>
              <CardTitle>按类型汇总</CardTitle>
            </CardHeader>
            <div className="grid gap-3 p-4 sm:grid-cols-2 xl:grid-cols-4">
              {dash.data.files.by_kind.map((k) => (
                <div key={k.kind} className="rounded-md border border-border px-3 py-2">
                  <div className="text-[11px] text-muted-foreground">{kindLabel(k.kind)}</div>
                  <div className="mt-0.5 text-[15px] font-semibold tabular-nums">{formatNumber(k.files)}</div>
                  <div className="text-[11px] text-muted-foreground">{formatBytes(k.bytes)}</div>
                </div>
              ))}
            </div>
          </Card>
        )}
      </div>
    </div>
  );
}

function Tile({
  label,
  value,
  sub,
  tone = "default",
}: {
  label: string;
  value: string;
  sub: string;
  tone?: "default" | "success" | "primary";
}) {
  const color = { default: "text-foreground", success: "text-success", primary: "text-primary" }[tone];
  return (
    <Card className="px-4 py-3">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className={cn("mt-0.5 text-[19px] font-semibold tabular-nums", color)}>{value}</div>
      <div className="text-[11px] text-muted-foreground">{sub}</div>
    </Card>
  );
}
