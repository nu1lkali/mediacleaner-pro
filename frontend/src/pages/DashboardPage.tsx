import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import {
  AlertTriangle,
  ArrowRight,
  Copy,
  Eraser,
  FileImage,
  Film,
  HardDrive,
  Music,
  ScanLine,
  Sparkles,
  Trash2,
} from "lucide-react";
import { api } from "../services/api";
import { Badge, Card, CardBody, CardHeader, CardTitle, StatCard } from "../components/ui/card";
import { EmptyState, ErrorBox, Loading, PageHeader, Section } from "../components/ui/empty";
import { Progress } from "../components/ui/progress";
import { Button } from "../components/ui/button";
import { formatBytes, formatDate, formatNumber, percent, statusText, statusTone } from "../lib/utils";

export default function DashboardPage() {
  const dash = useQuery({ queryKey: ["dashboard"], queryFn: api.dashboard, refetchInterval: 15000 });
  const tips = useQuery({ queryKey: ["suggestions"], queryFn: api.suggestions, refetchInterval: 60000 });

  if (dash.isLoading) return <Loading />;
  if (dash.error) return <ErrorBox message={(dash.error as Error).message} onRetry={() => dash.refetch()} />;

  const d = dash.data!;
  const dup = d.duplicates;
  const noRoots = d.roots.length === 0;
  const notScanned = d.files.total === 0;

  return (
    <div className="pb-10">
      <PageHeader
        title="总览"
        desc="媒体库现状与可回收空间一览。所有删除操作默认进入隔离区，可随时恢复。"
        actions={
          <>
            <Link to="/clean">
              <Button variant="outline" size="sm" icon={<Eraser className="h-3.5 w-3.5" />}>
                清理任务
              </Button>
            </Link>
            <Link to="/scan">
              <Button variant="primary" size="sm" icon={<ScanLine className="h-3.5 w-3.5" />}>
                新建扫描
              </Button>
            </Link>
          </>
        }
      />

      <div className="space-y-4 px-4 sm:px-5">
        {(noRoots || notScanned) && (
          <Card className="border-primary/30 bg-primary/[0.04]">
            <CardBody className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-start gap-3">
                <div className="rounded-md bg-primary/10 p-2 text-primary">
                  <Sparkles className="h-4 w-4" />
                </div>
                <div>
                  <div className="text-[13px] font-medium">
                    {noRoots ? "还没有添加存储卷" : "媒体库尚未索引"}
                  </div>
                  <div className="mt-0.5 text-[12px] text-muted-foreground">
                    {noRoots
                      ? "先添加一个 NAS 目录，然后运行一次「极速」扫描（不调用 ffmpeg，几分钟出结果）。"
                      : "已有存储卷但还没有扫描记录。建议先跑「极速：仅完全重复」，确认索引链路正常。"}
                  </div>
                </div>
              </div>
              <Link to={noRoots ? "/settings" : "/scan"}>
                <Button variant="primary" size="sm">
                  {noRoots ? "去添加存储卷" : "立即扫描"} <ArrowRight className="ml-1 h-3.5 w-3.5" />
                </Button>
              </Link>
            </CardBody>
          </Card>
        )}

        {/* 指标卡片 */}
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard
            label="已索引文件"
            value={formatNumber(d.files.total)}
            hint={`${formatBytes(d.files.bytes)} · ${formatNumber(d.files.videos)} 视频 / ${formatNumber(
              d.files.images,
            )} 图片`}
            icon={<HardDrive className="h-4 w-4" />}
            tone="primary"
          />
          <StatCard
            label="完全重复可回收"
            value={formatBytes(dup.exact_bytes)}
            hint={`${formatNumber(dup.exact_groups)} 组 · ${formatNumber(dup.exact_files)} 个多余文件`}
            icon={<Copy className="h-4 w-4" />}
            tone="warn"
          />
          <StatCard
            label="相似媒体可回收"
            value={formatBytes(dup.image_bytes + dup.video_bytes)}
            hint={`图片 ${formatNumber(dup.image_groups)} 组 · 视频 ${formatNumber(dup.video_groups)} 组`}
            icon={<Film className="h-4 w-4" />}
            tone="success"
          />
          <StatCard
            label="隔离区占用"
            value={formatBytes(d.quarantine.bytes)}
            hint={`${formatNumber(d.quarantine.count)} 个文件待确认`}
            icon={<Trash2 className="h-4 w-4" />}
            tone={d.quarantine.count > 0 ? "danger" : "default"}
          />
        </div>

        <div className="grid gap-4 xl:grid-cols-3">
          {/* 可回收构成 */}
          <Card className="xl:col-span-2">
            <CardHeader
              action={
                <Link to="/duplicates" className="text-[12px] text-primary hover:underline">
                  查看明细
                </Link>
              }
            >
              <CardTitle hint="按重复类型拆分，柱长表示可释放空间占比">可回收空间构成</CardTitle>
            </CardHeader>
            <CardBody className="space-y-3.5">
              <ReclaimRow
                label="完全重复"
                desc="内容相同的副本（全文 BLAKE3 或清理前逐字节复验）"
                bytes={dup.exact_bytes}
                files={dup.exact_files}
                groups={dup.exact_groups}
                total={dup.reclaimable || 1}
                tone="primary"
                to="/duplicates"
              />
              <ReclaimRow
                label="相似图片"
                desc="同图不同尺寸/压缩（pHash 感知哈希）"
                bytes={dup.image_bytes}
                files={dup.image_files}
                groups={dup.image_groups}
                total={dup.reclaimable || 1}
                tone="success"
                to="/similar-image"
              />
              <ReclaimRow
                label="相似视频"
                desc="转码/换封装/改码率（关键帧 pHash）"
                bytes={dup.video_bytes}
                files={dup.video_files}
                groups={dup.video_groups}
                total={dup.reclaimable || 1}
                tone="warn"
                to="/similar-video"
              />
              <div className="flex items-center justify-between border-t border-border pt-3">
                <div className="text-[12px] text-muted-foreground">合计可回收</div>
                <div className="text-[15px] font-semibold tabular-nums text-primary">
                  {formatBytes(dup.reclaimable)}
                </div>
              </div>
            </CardBody>
          </Card>

          {/* 媒体构成 */}
          <Card>
            <CardHeader>
              <CardTitle hint="按类型统计索引到的文件">媒体构成</CardTitle>
            </CardHeader>
            <CardBody className="space-y-3">
              <KindBar icon={<Film className="h-3.5 w-3.5" />} label="视频" files={d.files.videos}
                bytes={d.files.by_kind.find((k) => k.kind === "video")?.bytes ?? 0} total={d.files.bytes} />
              <KindBar icon={<FileImage className="h-3.5 w-3.5" />} label="图片" files={d.files.images}
                bytes={d.files.by_kind.find((k) => k.kind === "image")?.bytes ?? 0} total={d.files.bytes} />
              <KindBar icon={<Music className="h-3.5 w-3.5" />} label="音频" files={d.files.audio}
                bytes={d.files.by_kind.find((k) => k.kind === "audio")?.bytes ?? 0} total={d.files.bytes} />
              <KindBar icon={<HardDrive className="h-3.5 w-3.5" />} label="文档" files={d.files.docs}
                bytes={d.files.by_kind.find((k) => k.kind === "document")?.bytes ?? 0} total={d.files.bytes} />
            </CardBody>
          </Card>
        </div>

        <div className="grid gap-4 xl:grid-cols-2">
          {/* 最近扫描 */}
          <Card>
            <CardHeader action={<Link to="/history" className="text-[12px] text-primary hover:underline">全部</Link>}>
              <CardTitle hint="最近 5 次扫描">扫描活动</CardTitle>
            </CardHeader>
            {d.recent_scans.length === 0 ? (
              <EmptyState icon={<ScanLine className="h-5 w-5" />} title="还没有扫描记录"
                desc="运行一次扫描，索引媒体库并生成重复分组。" className="py-10" />
            ) : (
              <div className="divide-y divide-border/70">
                {d.recent_scans.map((s) => (
                  <div key={s.id} className="flex items-center gap-3 px-4 py-2.5">
                    <Badge tone={statusTone(s.status)}>{statusText[s.status] ?? s.status}</Badge>
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[13px]">{s.name}</div>
                      <div className="mt-0.5 text-[11px] text-muted-foreground">
                        {formatNumber(s.files_seen)} 文件 · {formatBytes(s.bytes_scanned)}
                        {s.error_count > 0 ? ` · ${s.error_count} 个错误` : ""}
                      </div>
                    </div>
                    <div className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
                      {formatDate(s.finished_at ?? s.queued_at)}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Card>

          {/* 建议 */}
          <Card>
            <CardHeader action={<Link to="/stats" className="text-[12px] text-primary hover:underline">统计</Link>}>
              <CardTitle hint="基于当前索引自动发现">优化建议</CardTitle>
            </CardHeader>
            {tips.isLoading ? (
              <Loading label="分析中…" className="py-10" />
            ) : !tips.data?.items.length ? (
              <EmptyState icon={<Sparkles className="h-5 w-5" />} title="暂无建议" desc="索引数据完善后会自动给出清理建议。" className="py-10" />
            ) : (
              <div className="divide-y divide-border/70">
                {tips.data.items.slice(0, 5).map((s) => (
                  <div key={s.key} className="flex items-start gap-2.5 px-4 py-2.5">
                    <div
                      className={
                        s.severity === "warn"
                          ? "mt-0.5 text-warning"
                          : s.severity === "ok"
                            ? "mt-0.5 text-success"
                            : "mt-0.5 text-primary"
                      }
                    >
                      {s.severity === "warn" ? (
                        <AlertTriangle className="h-3.5 w-3.5" />
                      ) : (
                        <Sparkles className="h-3.5 w-3.5" />
                      )}
                    </div>
                    <div className="min-w-0">
                      <div className="text-[12.5px] font-medium">{s.title}</div>
                      <div className="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">{s.detail}</div>
                    </div>
                    {s.bytes > 0 ? (
                      <div className="ml-auto shrink-0 text-[11px] tabular-nums text-muted-foreground">
                        {formatBytes(s.bytes)}
                      </div>
                    ) : null}
                  </div>
                ))}
              </div>
            )}
          </Card>
        </div>
      </div>
    </div>
  );
}

function ReclaimRow({
  label,
  desc,
  bytes,
  files,
  groups,
  total,
  tone,
  to,
}: {
  label: string;
  desc: string;
  bytes: number;
  files: number;
  groups: number;
  total: number;
  tone: "primary" | "success" | "warn";
  to: string;
}) {
  const p = percent(bytes, total);
  return (
    <Link to={to} className="block rounded-md px-1 py-1 transition-colors hover:bg-muted/60">
      <div className="flex items-baseline justify-between gap-2">
        <div className="flex items-baseline gap-2">
          <span className="text-[13px] font-medium">{label}</span>
          <span className="text-[11px] text-muted-foreground">{desc}</span>
        </div>
        <div className="shrink-0 text-[13px] font-semibold tabular-nums">{formatBytes(bytes)}</div>
      </div>
      <div className="mt-1.5 flex items-center gap-2.5">
        <Progress value={p} tone={tone} height={5} className="flex-1" />
        <span className="w-[112px] shrink-0 text-right text-[11px] tabular-nums text-muted-foreground">
          {formatNumber(groups)} 组 / {formatNumber(files)} 个
        </span>
      </div>
    </Link>
  );
}

function KindBar({
  icon,
  label,
  files,
  bytes,
  total,
}: {
  icon: React.ReactNode;
  label: string;
  files: number;
  bytes: number;
  total: number;
}) {
  return (
    <div>
      <div className="flex items-center justify-between text-[12px]">
        <span className="flex items-center gap-1.5 text-muted-foreground">
          {icon}
          {label}
        </span>
        <span className="tabular-nums">
          {formatNumber(files)} <span className="text-muted-foreground">/ {formatBytes(bytes)}</span>
        </span>
      </div>
      <Progress value={percent(bytes, total)} height={4} className="mt-1.5" tone="primary" />
    </div>
  );
}
