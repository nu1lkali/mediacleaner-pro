import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ChevronDown,
  ChevronRight,
  CirclePause,
  CirclePlay,
  Copy,
  Film,
  FolderPlus,
  Gauge,
  HardDrive,
  Images,
  Info,
  Pause,
  Play,
  RefreshCw,
  ScanLine,
  Trash2,
  X,
  Zap,
} from "lucide-react";
import { api } from "../services/api";
import type { Root, ScanProfile, ScanProgress, ScanTask } from "../services/types";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Checkbox, Field, Input, Select, Switch, TagInput } from "../components/ui/field";
import { EmptyState, Loading, PageHeader, Section } from "../components/ui/empty";
import { Progress, Ring } from "../components/ui/progress";
import { DirectoryPicker } from "../components/DirectoryPicker";
import { cn, formatBytes, formatDate, formatNumber, phaseText, statusText, statusTone } from "../lib/utils";

type Detect = { exact: boolean; image: boolean; video: boolean };

const PROFILE_META: Record<
  string,
  { icon: React.ReactNode; tone: string; badge: string; speed: string }
> = {
  fast: {
    icon: <Zap className="h-4 w-4" />,
    tone: "text-success",
    badge: "最快 · 无需 ffmpeg",
    speed: "10 万文件约 3-8 分钟",
  },
  standard: {
    icon: <Images className="h-4 w-4" />,
    tone: "text-primary",
    badge: "推荐",
    speed: "图片越多越慢",
  },
  full: {
    icon: <Film className="h-4 w-4" />,
    tone: "text-warning",
    badge: "需 ffmpeg",
    speed: "与视频数量线性相关",
  },
};

export default function ScanPage() {
  const qc = useQueryClient();
  const profiles = useQuery({ queryKey: ["scan-profiles"], queryFn: api.scanProfiles });
  const roots = useQuery({ queryKey: ["roots"], queryFn: api.roots });
  const scans = useQuery({ queryKey: ["scans"], queryFn: () => api.scans(20, 0), refetchInterval: 4000 });
  const current = useQuery({ queryKey: ["current-scan"], queryFn: api.currentScan, refetchInterval: 3000 });

  const [profile, setProfile] = useState<string>("fast");
  const [detect, setDetect] = useState<Detect>({ exact: true, image: false, video: false });
  const [rootIDs, setRootIDs] = useState<number[]>([]);
  const [extraPaths, setExtraPaths] = useState<string[]>([]);
  const [onlyKinds, setOnlyKinds] = useState<string[]>([]);
  const [minSizeMB, setMinSizeMB] = useState(0);
  const [maxDepth, setMaxDepth] = useState(0);
  const [excludeDirs, setExcludeDirs] = useState<string[]>([]);
  const [excludeNames, setExcludeNames] = useState<string[]>([]);
  const [threads, setThreads] = useState(0);
  const [limitMBps, setLimitMBps] = useState(0);
  const [deepProbe, setDeepProbe] = useState(false);
  const [followSymlinks, setFollowSymlinks] = useState(false);
  const [includeOther, setIncludeOther] = useState(false);
  const [fullRescan, setFullRescan] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const [name, setName] = useState("");
  const [activeId, setActiveId] = useState<number | null>(null);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerTarget, setPickerTarget] = useState<"paths" | "exclude">("paths");

  const caps = profiles.data?.capabilities ?? {};
  const ffmpegOK = Boolean(caps.ffmpeg);
  const rootList = roots.data?.items ?? [];

  // 首次拿到存储卷时默认全选
  const initedRef = useRef(false);
  useEffect(() => {
    if (initedRef.current || rootList.length === 0) return;
    initedRef.current = true;
    setRootIDs(rootList.filter((r) => r.enabled).map((r) => r.id));
  }, [rootList]);

  // 运行中的任务自动聚焦
  useEffect(() => {
    if (current.data?.task && activeId === null) setActiveId(current.data.task.id);
  }, [current.data, activeId]);

  const applyProfile = (id: string) => {
    setProfile(id);
    const p = (profiles.data?.items ?? []).find((x) => x.id === id);
    if (!p) return;
    setDetect({
      exact: p.detect_exact,
      image: p.detect_image,
      video: p.detect_video && ffmpegOK,
    });
  };

  // 手动改开关后，profile 变为"自定义"
  const setOne = (key: keyof Detect, v: boolean) => {
    const next = { ...detect, [key]: v };
    if ((key === "image" || key === "video") && v) next.exact = true; // 相似检测依赖全文校验
    if (!next.exact && !next.image && !next.video) next.exact = true;
    setDetect(next);
    const match = (profiles.data?.items ?? []).find(
      (p) => p.detect_exact === next.exact && p.detect_image === next.image && p.detect_video === next.video,
    );
    setProfile(match?.id ?? "custom");
  };

  const create = useMutation({
    mutationFn: () =>
      api.createScan({
        name: name.trim() || undefined,
        mode: fullRescan ? "full" : "incremental",
        profile: profile === "custom" ? "fast" : profile,
        root_ids: rootIDs,
        extra_paths: extraPaths,
        detect_exact: detect.exact,
        detect_similar_image: detect.image,
        detect_similar_video: detect.video,
        only_kinds: onlyKinds,
        min_file_size: Math.round(minSizeMB * 1024 * 1024),
        exclude_dirs: excludeDirs,
        exclude_names: excludeNames,
        max_depth: maxDepth,
        threads,
        limit_mbps: limitMBps,
        deep_probe: deepProbe,
        follow_symlinks: followSymlinks,
        include_other: includeOther,
      }),
    onSuccess: (task) => {
      setActiveId(task.id);
      setName("");
      qc.invalidateQueries({ queryKey: ["scans"] });
      qc.invalidateQueries({ queryKey: ["current-scan"] });
    },
  });

  const control = useMutation({
    mutationFn: ({ id, action }: { id: number; action: "pause" | "resume" | "cancel" }) =>
      api.controlScan(id, action),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scans"] });
      qc.invalidateQueries({ queryKey: ["current-scan"] });
    },
  });

  const activeTask =
    scans.data?.items.find((t) => t.id === activeId) ?? (current.data?.task?.id === activeId ? current.data.task : null);

  const noneSelected = rootIDs.length === 0 && extraPaths.length === 0;
  const willDoNothing = !detect.exact && !detect.image && !detect.video;

  return (
    <div className="pb-10">
      <PageHeader
        title="媒体扫描"
        desc="三种检测能力可以完全独立运行：只查完全重复不必解码任何媒体，需要相似视频才启用关键帧。"
        actions={
          <Button
            variant="ghost"
            size="sm"
            icon={<RefreshCw className="h-3.5 w-3.5" />}
            onClick={() => {
              qc.invalidateQueries({ queryKey: ["scans"] });
              qc.invalidateQueries({ queryKey: ["current-scan"] });
            }}
          >
            刷新
          </Button>
        }
      />

      <div className="space-y-4 px-4 sm:px-5">
        {/* 运行中的任务 */}
        {activeTask && <ActiveTaskCard task={activeTask} onControl={control.mutate} busy={control.isPending} />}

        {/* 预设 */}
        <Card>
          <CardHeader>
            <CardTitle hint="预设只决定默认开关，选完仍可逐项微调">选择扫描模式</CardTitle>
          </CardHeader>
          <CardBody>
            {profiles.isLoading ? (
              <Loading label="读取能力清单…" className="py-6" />
            ) : (
              <div className="grid gap-2.5 md:grid-cols-3">
                {(profiles.data?.items ?? []).map((p) => (
                  <ProfileCard
                    key={p.id}
                    profile={p}
                    selected={profile === p.id}
                    disabled={p.need_ffmpeg && !ffmpegOK}
                    onClick={() => applyProfile(p.id)}
                  />
                ))}
              </div>
            )}
            {!ffmpegOK && (
              <div className="mt-3 flex items-start gap-2 rounded-md border border-warning/30 bg-warning/8 px-3 py-2 text-[12px] text-warning">
                <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span>
                  未检测到 ffmpeg，视频关键帧相似度不可用。其余能力（完全重复、相似图片）为纯 Go 实现，不受影响。
                  在群晖上安装 ffmpeg 后重启容器即可解锁「深度」模式。
                </span>
              </div>
            )}
          </CardBody>
        </Card>

        <div className="grid gap-4 xl:grid-cols-3">
          {/* 检测能力 */}
          <Card className="xl:col-span-2">
            <CardHeader>
              <CardTitle hint="任一能力都可单独开启，互不依赖（相似检测会自动附带全文校验）">
                检测能力
              </CardTitle>
            </CardHeader>
            <CardBody className="space-y-3">
              <DetectRow
                icon={<Copy className="h-3.5 w-3.5" />}
                title="完全重复（逐字节一致）"
                desc="元数据索引 → 采样指纹 → 全文 SHA-256 校验。不解码媒体、不调用 ffmpeg。"
                checked={detect.exact}
                onChange={(v) => setOne("exact", v)}
                cost="低"
              />
              <DetectRow
                icon={<Images className="h-3.5 w-3.5" />}
                title="相似图片（pHash 感知哈希）"
                desc="纯 Go 解码缩略图计算 64 位指纹，识别同图不同尺寸/不同压缩/加过水印的版本。"
                checked={detect.image}
                onChange={(v) => setOne("image", v)}
                cost="中"
              />
              <DetectRow
                icon={<Film className="h-3.5 w-3.5" />}
                title="相似视频（关键帧指纹）"
                desc="ffmpeg 均匀抽帧后计算 pHash，多帧匹配判定同一视频。这是唯一会解码视频的能力。"
                checked={detect.video}
                onChange={(v) => setOne("video", v)}
                cost="高"
                disabled={!ffmpegOK}
                disabledHint={!ffmpegOK ? "需要 ffmpeg" : undefined}
              />
              {willDoNothing && (
                <div className="rounded-md border border-destructive/30 bg-destructive/8 px-3 py-2 text-[12px] text-destructive">
                  至少需要开启一项检测能力。
                </div>
              )}
            </CardBody>
          </Card>

          {/* 扫描范围 */}
          <Card>
            <CardHeader>
              <CardTitle hint="不选则使用全部已启用的存储卷">扫描范围</CardTitle>
            </CardHeader>
            <CardBody className="space-y-3">
              {roots.isLoading ? (
                <Loading label="读取存储卷…" className="py-4" />
              ) : rootList.length === 0 ? (
                <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[12px] text-muted-foreground">
                  还没有注册存储卷。可以在下方「手动指定目录」直接填写 NAS 路径，任务会自动注册。
                </div>
              ) : (
                <div className="space-y-1.5">
                  {rootList.map((r) => (
                    <label
                      key={r.id}
                      className={cn(
                        "flex cursor-pointer items-start gap-2 rounded-md border px-2.5 py-2 transition-colors",
                        rootIDs.includes(r.id) ? "border-primary/40 bg-primary/[0.04]" : "border-border hover:bg-muted/50",
                      )}
                    >
                      <input
                        type="checkbox"
                        checked={rootIDs.includes(r.id)}
                        onChange={(e) =>
                          setRootIDs((prev) =>
                            e.target.checked ? [...prev, r.id] : prev.filter((x) => x !== r.id),
                          )
                        }
                        className="mt-0.5 h-3.5 w-3.5 accent-primary"
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-1.5">
                          <span className="truncate text-[12.5px] font-medium">{r.name}</span>
                          {!r.enabled && <Badge tone="outline">已停用</Badge>}
                          {r.volume && <Badge tone="outline">{r.volume}</Badge>}
                        </div>
                        <div className="truncate text-[11px] text-muted-foreground">{r.real_path}</div>
                      </div>
                    </label>
                  ))}
                </div>
              )}

              <Field label="手动指定目录" hint="不属于已注册卷，回车添加；也可点「浏览」在容器内挑选">
                <TagInput
                  value={extraPaths}
                  onChange={setExtraPaths}
                  placeholder="/media/photo/2024"
                  mono
                />
              </Field>
              <Button
                size="sm"
                variant="outline"
                icon={<FolderPlus className="h-3.5 w-3.5" />}
                className="w-full"
                onClick={() => {
                  setPickerTarget("paths");
                  setPickerOpen(true);
                }}
              >
                浏览容器内目录并加入扫描
              </Button>
              <p className="text-[11px] leading-relaxed text-muted-foreground">
                选中的目录会作为本次任务的扫描范围；若该目录尚未注册存储卷，后端会自动注册（默认「允许清理」关闭）。
              </p>

              <Field label="任务名称" hint="留空自动生成">
                <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例：清理 2024 年手机备份" />
              </Field>
            </CardBody>
          </Card>
        </div>

        {/* 高级选项 */}
        <Card>
          <button
            onClick={() => setAdvanced((v) => !v)}
            className="flex w-full items-center justify-between px-4 py-3 text-left"
          >
            <div>
              <div className="flex items-center gap-1.5 text-sm font-semibold">
                {advanced ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
                高级选项
              </div>
              <div className="mt-0.5 pl-5 text-[11.5px] text-muted-foreground">
                过滤条件、排除规则、并发与限速 —— 用于手动精调一次任务
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-1.5">
              {onlyKinds.length > 0 && <Badge tone="info">{onlyKinds.length} 种类型</Badge>}
              {minSizeMB > 0 && <Badge tone="outline">≥ {minSizeMB} MB</Badge>}
              {excludeDirs.length > 0 && <Badge tone="outline">{excludeDirs.length} 个排除目录</Badge>}
              {limitMBps > 0 && <Badge tone="outline">限速 {limitMBps} MB/s</Badge>}
            </div>
          </button>

          {advanced && (
            <CardBody className="border-t border-border pt-4">
              <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
                <Field label="只收录媒体类型" hint="留空=全部">
                  <div className="flex flex-wrap gap-1.5">
                    {[
                      { k: "video", l: "视频" },
                      { k: "image", l: "图片" },
                      { k: "audio", l: "音频" },
                      { k: "document", l: "文档" },
                    ].map((o) => (
                      <button
                        key={o.k}
                        onClick={() =>
                          setOnlyKinds((prev) =>
                            prev.includes(o.k) ? prev.filter((x) => x !== o.k) : [...prev, o.k],
                          )
                        }
                        className={cn(
                          "rounded-md border px-2 py-1 text-[12px] transition-colors",
                          onlyKinds.includes(o.k)
                            ? "border-primary bg-primary/10 text-primary"
                            : "border-border text-muted-foreground hover:bg-muted",
                        )}
                      >
                        {o.l}
                      </button>
                    ))}
                  </div>
                </Field>

                <Field label="最小文件大小 (MB)" hint="0 = 不限">
                  <Input
                    type="number"
                    min={0}
                    value={minSizeMB || ""}
                    onChange={(e) => setMinSizeMB(Number(e.target.value) || 0)}
                    placeholder="0"
                  />
                </Field>

                <Field label="最大扫描深度" hint="0 = 不限">
                  <Input
                    type="number"
                    min={0}
                    value={maxDepth || ""}
                    onChange={(e) => setMaxDepth(Number(e.target.value) || 0)}
                    placeholder="0"
                  />
                </Field>

                <Field label="并发线程" hint={threads ? undefined : "0 = 使用系统默认"}>
                  <Input
                    type="number"
                    min={0}
                    value={threads || ""}
                    onChange={(e) => setThreads(Number(e.target.value) || 0)}
                    placeholder="自动"
                  />
                </Field>

                <Field label="排除目录" hint="路径前缀或目录名，可点右侧浏览选择">
                  <div className="flex gap-1.5">
                    <div className="min-w-0 flex-1">
                      <TagInput value={excludeDirs} onChange={setExcludeDirs} placeholder="@eaDir、#recycle" mono />
                    </div>
                    <Button
                      size="sm"
                      variant="outline"
                      className="h-8 shrink-0 px-2"
                      icon={<FolderPlus className="h-3.5 w-3.5" />}
                      onClick={() => {
                        setPickerTarget("exclude");
                        setPickerOpen(true);
                      }}
                    />
                  </div>
                </Field>

                <Field label="排除文件名" hint="支持通配符 *">
                  <TagInput value={excludeNames} onChange={setExcludeNames} placeholder="*.tmp、Thumbs.db" mono />
                </Field>

                <Field label="读取限速 (MB/s)" hint="0 = 不限，群晖低负载建议 50-100">
                  <Input
                    type="number"
                    min={0}
                    value={limitMBps || ""}
                    onChange={(e) => setLimitMBps(Number(e.target.value) || 0)}
                    placeholder="0"
                  />
                </Field>

                <div className="space-y-2.5 pt-1">
                  <Switch
                    checked={deepProbe}
                    onChange={setDeepProbe}
                    label="深度探测元数据"
                    hint="用 ffprobe 读精确信息，更慢"
                    disabled={!caps.ffprobe}
                  />
                  <Switch checked={followSymlinks} onChange={setFollowSymlinks} label="跟随符号链接" />
                  <Switch checked={includeOther} onChange={setIncludeOther} label="收录非媒体文件" />
                  <Switch
                    checked={fullRescan}
                    onChange={setFullRescan}
                    label="全量重扫"
                    hint="忽略已有索引，重新读取所有文件"
                  />
                </div>
              </div>
            </CardBody>
          )}
        </Card>

        {/* 提交 */}
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card px-4 py-3">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px] text-muted-foreground">
            <span>
              范围：
              <span className="font-medium text-foreground">
                {rootIDs.length} 个存储卷{extraPaths.length ? ` + ${extraPaths.length} 个目录` : ""}
              </span>
            </span>
            <span>
              能力：
              <span className="font-medium text-foreground">
                {[detect.exact && "完全重复", detect.image && "相似图片", detect.video && "相似视频"]
                  .filter(Boolean)
                  .join(" + ") || "未开启"}
              </span>
            </span>
            <span>模式：{fullRescan ? "全量" : "增量"}</span>
          </div>
          <div className="flex items-center gap-2">
            {noneSelected && (
              <span className="text-[11.5px] text-warning">请选择扫描范围或填写目录</span>
            )}
            {create.isError && (
              <span className="max-w-[320px] truncate text-[11.5px] text-destructive">
                {(create.error as Error).message}
              </span>
            )}
            <Button
              variant="primary"
              size="md"
              icon={<ScanLine className="h-4 w-4" />}
              loading={create.isPending}
              disabled={noneSelected || willDoNothing}
              onClick={() => create.mutate()}
            >
              开始扫描
            </Button>
          </div>
        </div>

        {/* 历史 */}
        <Card>
          <CardHeader>
            <CardTitle hint="最近 20 条">扫描记录</CardTitle>
          </CardHeader>
          {scans.isLoading ? (
            <Loading />
          ) : !scans.data?.items.length ? (
            <EmptyState icon={<ScanLine className="h-5 w-5" />} title="还没有扫描任务" className="py-10" />
          ) : (
            <div className="overflow-x-auto">
              <table className="table">
                <thead>
                  <tr>
                    <th className="w-[64px]">ID</th>
                    <th>名称</th>
                    <th className="w-[140px]">能力</th>
                    <th className="w-[96px]">状态</th>
                    <th className="w-[110px]">文件</th>
                    <th className="w-[110px]">数据量</th>
                    <th className="w-[130px]">结束时间</th>
                    <th className="w-[150px]" />
                  </tr>
                </thead>
                <tbody>
                  {scans.data.items.map((t) => (
                    <tr key={t.id} className={cn(t.id === activeId && "bg-primary/[0.04]")}>
                      <td className="mono text-muted-foreground">#{t.id}</td>
                      <td className="max-w-[240px] truncate">{t.name}</td>
                      <td>
                        <div className="flex flex-wrap gap-1">
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
                      <td className="tabular-nums text-muted-foreground">
                        {formatDate(t.finished_at ?? t.queued_at)}
                      </td>
                      <td>
                        <div className="flex items-center justify-end gap-1">
                          <Button size="sm" variant="ghost" onClick={() => setActiveId(t.id)}>
                            查看
                          </Button>
                          {t.status === "running" || t.status === "paused" ? (
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => control.mutate({ id: t.id, action: "cancel" })}
                            >
                              取消
                            </Button>
                          ) : (
                            <Button
                              size="sm"
                              variant="ghost"
                              className="text-destructive hover:bg-destructive/10"
                              icon={<Trash2 className="h-3.5 w-3.5" />}
                              onClick={async () => {
                                await api.deleteScan(t.id);
                                if (activeId === t.id) setActiveId(null);
                                qc.invalidateQueries({ queryKey: ["scans"] });
                              }}
                            />
                          )}
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </div>

      <DirectoryPicker
        open={pickerOpen}
        onClose={() => setPickerOpen(false)}
        title={pickerTarget === "exclude" ? "选择要排除的目录" : "选择扫描目录"}
        desc={pickerTarget === "exclude" ? "选中的目录会作为前缀被跳过" : "从已注册存储卷或挂载的媒体目录中选择"}
        pickLabel={pickerTarget === "exclude" ? "排除此目录" : "加入扫描范围"}
        onPick={(p) => {
          if (pickerTarget === "exclude") {
            setExcludeDirs((prev) => (prev.includes(p) ? prev : [...prev, p]));
            setAdvanced(true);
          } else {
            setExtraPaths((prev) => (prev.includes(p) ? prev : [...prev, p]));
          }
          setPickerOpen(false);
        }}
      />
    </div>
  );
}

function ProfileCard({
  profile,
  selected,
  disabled,
  onClick,
}: {
  profile: ScanProfile;
  selected: boolean;
  disabled?: boolean;
  onClick: () => void;
}) {
  const meta = PROFILE_META[profile.id] ?? PROFILE_META.fast;
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className={cn(
        "group relative rounded-lg border p-3 text-left transition-all",
        selected ? "border-primary bg-primary/[0.05] ring-1 ring-primary/25" : "border-border hover:border-primary/40",
        disabled && "cursor-not-allowed opacity-55 hover:border-border",
      )}
    >
      <div className="flex items-center gap-2">
        <span className={cn("rounded-md bg-muted p-1.5", meta.tone)}>{meta.icon}</span>
        <span className="text-[13px] font-medium">{profile.name}</span>
      </div>
      <p className="mt-2 text-[11.5px] leading-relaxed text-muted-foreground">{profile.desc}</p>
      <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
        <Badge tone={profile.need_ffmpeg ? "warn" : "success"}>{meta.badge}</Badge>
        <span className="text-[10.5px] text-muted-foreground">{meta.speed}</span>
      </div>
      <div className="mt-2 flex gap-1">
        {profile.detect_exact && <Chip>完全重复</Chip>}
        {profile.detect_image && <Chip>图片</Chip>}
        {profile.detect_video && <Chip>视频</Chip>}
      </div>
      {selected && (
        <span className="absolute right-2.5 top-2.5 flex h-4 w-4 items-center justify-center rounded-full bg-primary text-[9px] font-bold text-primary-foreground">
          ✓
        </span>
      )}
    </button>
  );
}

function Chip({ children }: { children: React.ReactNode }) {
  return (
    <span className="rounded bg-muted px-1.5 py-0.5 text-[10.5px] text-muted-foreground">{children}</span>
  );
}

function DetectRow({
  icon,
  title,
  desc,
  checked,
  onChange,
  cost,
  disabled,
  disabledHint,
}: {
  icon: React.ReactNode;
  title: string;
  desc: string;
  checked: boolean;
  onChange: (v: boolean) => void;
  cost: "低" | "中" | "高";
  disabled?: boolean;
  disabledHint?: string;
}) {
  const costTone = cost === "低" ? "success" : cost === "中" ? "info" : "warn";
  return (
    <div
      className={cn(
        "flex items-start gap-3 rounded-md border px-3 py-2.5 transition-colors",
        checked ? "border-primary/35 bg-primary/[0.035]" : "border-border",
        disabled && "opacity-60",
      )}
    >
      <span className={cn("mt-0.5 rounded p-1.5", checked ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground")}>
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-[13px] font-medium">{title}</span>
          <Badge tone={costTone as "success" | "info" | "warn"}>开销 {cost}</Badge>
          {disabledHint && <Badge tone="outline">{disabledHint}</Badge>}
        </div>
        <p className="mt-0.5 text-[11.5px] leading-relaxed text-muted-foreground">{desc}</p>
      </div>
      <div className="pt-0.5">
        <Switch checked={checked} onChange={onChange} disabled={disabled} />
      </div>
    </div>
  );
}

/* ---------------- 运行中任务 ---------------- */

function ActiveTaskCard({
  task,
  onControl,
  busy,
}: {
  task: ScanTask;
  onControl: (v: { id: number; action: "pause" | "resume" | "cancel" }) => void;
  busy: boolean;
}) {
  const [live, setLive] = useState<ScanProgress | null>(null);
  const [liveAt, setLiveAt] = useState(0);
  const live_ref = useRef<EventSource | null>(null);
  const running = task.status === "running" || task.status === "paused" || task.status === "pending";

  // 运行中打开 SSE：断线后自动重连；一断开就丢弃本地快照，让外部每 3 秒轮询的
  // task 接管，避免界面停在旧数字直到用户手动刷新页面。
  useEffect(() => {
    if (!running) {
      live_ref.current?.close();
      live_ref.current = null;
      setLive(null);
      return;
    }

    let disposed = false;
    let finished = false;
    let retry: number | undefined;
    let es: EventSource | null = null;

    const connect = () => {
      if (disposed || finished) return;
      es = new EventSource(api.scanEventsUrl(task.id));
      live_ref.current = es;

      es.addEventListener("progress", (e) => {
        try {
          setLive(JSON.parse((e as MessageEvent).data) as ScanProgress);
          setLiveAt(Date.now());
        } catch {
          /* 忽略半包 */
        }
      });

      es.addEventListener("done", () => {
        finished = true;
        es?.close();
        live_ref.current = null;
      });

      es.onerror = () => {
        es?.close();
        live_ref.current = null;
        setLive(null);
        if (disposed || finished) return;
        // 3 秒后重连；期间界面用轮询数据顶上
        retry = window.setTimeout(connect, 3000);
      };
    };

    connect();
    return () => {
      disposed = true;
      if (retry) window.clearTimeout(retry);
      es?.close();
      live_ref.current = null;
    };
  }, [task.id, running]);

  // 超过 15 秒没收到任何事件就认为这条流哑了，改用轮询值
  const src = live && Date.now() - liveAt < 15000 ? live : null;

  const status = src?.status ?? task.status;
  const phase = src?.phase ?? task.phase;
  const files = src?.files_seen ?? task.files_seen;
  const bytes = src?.bytes_scanned ?? task.bytes_scanned;
  const errors = src?.error_count ?? task.error_count;
  const message = src?.message ?? task.message;
  const percent = src?.percent ?? (typeof task.progress?.percent === "number" ? task.progress.percent : 0);
  const currentPath = src?.current_path ?? task.current_path;
  const elapsed = src?.elapsed_ms ?? 0;

  const done = status === "succeeded" || status === "failed" || status === "canceled";

  return (
    <Card className={cn("overflow-hidden", !done && "border-primary/40")}>
      <CardHeader
        className={cn(!done && "bg-primary/[0.04]")}
        action={
          running ? (
            <div className="flex items-center gap-1.5">
              {status === "paused" ? (
                <Button
                  size="sm"
                  variant="outline"
                  icon={<Play className="h-3.5 w-3.5" />}
                  loading={busy}
                  onClick={() => onControl({ id: task.id, action: "resume" })}
                >
                  继续
                </Button>
              ) : (
                <Button
                  size="sm"
                  variant="outline"
                  icon={<Pause className="h-3.5 w-3.5" />}
                  loading={busy}
                  disabled={status === "pending"}
                  onClick={() => onControl({ id: task.id, action: "pause" })}
                >
                  暂停
                </Button>
              )}
              <Button size="sm" variant="ghost" className="text-destructive" onClick={() => onControl({ id: task.id, action: "cancel" })}>
                <X className="h-3.5 w-3.5" />
                取消
              </Button>
            </div>
          ) : (
            <Badge tone={statusTone(status)}>{statusText[status] ?? status}</Badge>
          )
        }
      >
        <CardTitle hint={`#${task.id} · ${task.mode === "full" ? "全量" : "增量"}扫描`}>
          {task.name}
        </CardTitle>
      </CardHeader>
      <CardBody>
        <div className="flex items-center gap-5">
          <Ring value={percent} size={68} stroke={6}>
            {done ? (status === "succeeded" ? "完成" : "结束") : `${Math.round(percent)}%`}
          </Ring>
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[13px] font-medium">
                {statusText[status] ?? status}
                {phase ? ` · ${phaseText[phase] ?? phase}` : ""}
              </span>
              {errors > 0 && <Badge tone="warn">{errors} 个错误</Badge>}
              {elapsed > 0 && <span className="text-[11px] text-muted-foreground">已用 {Math.round(elapsed / 1000)}s</span>}
            </div>
            {message ? <div className="mt-1 text-[12px] text-muted-foreground">{message}</div> : null}
            <Progress value={percent} className="mt-2" animated={!done} />
            <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1 text-[11.5px] text-muted-foreground sm:grid-cols-4">
              <Stat label="已扫描" value={formatNumber(files)} />
              <Stat label="已索引" value={formatNumber(src?.files_indexed ?? task.files_indexed)} />
              <Stat label="数据量" value={formatBytes(bytes)} />
              <Stat label="阶段" value={src ? `${src.phase_done}/${src.phase_total}` : "—"} />
            </div>
            {currentPath && !done ? (
              <div className="mt-2 truncate font-mono text-[10.5px] text-muted-foreground/80" title={currentPath}>
                {currentPath}
              </div>
            ) : null}
          </div>
        </div>
      </CardBody>
    </Card>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span>{label}</span>
      <span className="font-medium tabular-nums text-foreground">{value}</span>
    </div>
  );
}
