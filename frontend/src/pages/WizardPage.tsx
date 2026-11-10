import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCheck,
  ChevronDown,
  ChevronRight,
  Copy,
  Eraser,
  FolderOpen,
  FolderPlus,
  HardDrive,
  Images,
  Info,
  Loader2,
  Play,
  RefreshCw,
  ScanLine,
  ShieldCheck,
  Trash2,
  Zap,
} from "lucide-react";
import { api } from "../services/api";
import type {
  DupGroup,
  GroupSummaryItem,
  KeeperRuleDef,
  Root,
  ScanBatch,
  ScanTask,
  TrustAllResult,
} from "../services/types";
import { Thumb } from "../components/Thumb";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { ConfirmDialog } from "../components/ui/dialog";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui/empty";
import { Progress } from "../components/ui/progress";
import { Select } from "../components/ui/field";
import { DirectoryPicker } from "../components/DirectoryPicker";
import { cn, formatBytes, formatDate, formatNumber } from "../lib/utils";

type Step = 1 | 2 | 3 | 4;
type VerifyMode = "strict" | "quick" | "turbo";

const STEPS = ["设定规则与目录", "查找重复文件", "核对清单", "确认清理"];

const VERIFY_MODES: {
  id: VerifyMode;
  title: string;
  desc: string;
  badge: string;
  tone: "success" | "info" | "warn";
  icon: React.ReactNode;
}[] = [
  {
    id: "strict",
    title: "稳妥（推荐）",
    desc: "扫描时逐字节全文校验，最可靠；大文件多时会慢一些。分组标为「已校验」。",
    badge: "最可靠",
    tone: "success",
    icon: <ShieldCheck className="h-4 w-4" />,
  },
  {
    id: "quick",
    title: "快速",
    desc: "只比大小与 4 段采样，扫描快很多；执行清理前自动与保留副本逐字节复验，不一致就跳过。",
    badge: "推荐平衡",
    tone: "info",
    icon: <Zap className="h-4 w-4" />,
  },
  {
    id: "turbo",
    title: "极速（有风险）",
    desc: "扫描不读全文，清理时也不比对。同大小但内容不同的文件可能被误删，需二次确认。",
    badge: "高风险",
    tone: "warn",
    icon: <AlertTriangle className="h-4 w-4" />,
  },
];

/**
 * 一键清理向导：把「设定目录与规则 → 找重复 → 看清单 → 清理」放在同一页，
 * 新手不需要在扫描页、分组页、清理页之间来回跳。
 * 已有扫描结果时直接落在「核对清单」，避免每次进来都重新设置一遍。
 */
export default function WizardPage() {
  const qc = useQueryClient();
  const [step, setStep] = useState<Step>(1);
  const initedRef = useRef(false);
  const rootsInitedRef = useRef(false);

  // 第 1 步：规则与目录
  const [rootIDs, setRootIDs] = useState<number[]>([]);
  const [extraPaths, setExtraPaths] = useState<string[]>([]);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [withSimilarImage, setWithSimilarImage] = useState(false);
  const [withSimilarVideo, setWithSimilarVideo] = useState(false);
  const [keeperRule, setKeeperRule] = useState<string>("");
  const [verifyMode, setVerifyMode] = useState<VerifyMode>("quick");
  const [turboConfirmOpen, setTurboConfirmOpen] = useState(false);
  const [turboAck, setTurboAck] = useState(false);

  // 第 2/3/4 步
  const [activeScanId, setActiveScanId] = useState<number | null>(null);
  const [cleanTaskId, setCleanTaskId] = useState<number | null>(null);
  const [cleanConfirmOpen, setCleanConfirmOpen] = useState(false);
  const [trustConfirmOpen, setTrustConfirmOpen] = useState(false);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [notice, setNotice] = useState<string | null>(null);

  const roots = useQuery({ queryKey: ["roots"], queryFn: api.roots });
  const profiles = useQuery({ queryKey: ["scan-profiles"], queryFn: api.scanProfiles });
  const keeper = useQuery({
    queryKey: ["keeper-rule", "exact"],
    queryFn: () => api.keeperRule("exact"),
  });
  const batches = useQuery({ queryKey: ["batches"], queryFn: api.batches });
  const activeTaskId = batches.data?.active_task_id ?? 0;
  const activeBatch = batches.data?.items.find((b: ScanBatch) => b.active) ?? null;

  const caps = profiles.data?.capabilities ?? {};
  const ffmpegOK = Boolean(caps.ffmpeg);
  const rootList = roots.data?.items ?? [];
  const defs: KeeperRuleDef[] = keeper.data?.definitions ?? [];

  const summary = useQuery({
    queryKey: ["group-summary", activeTaskId],
    queryFn: api.groupSummary,
    enabled: step === 3,
  });
  const groups = useQuery({
    queryKey: ["wizard-groups", activeTaskId],
    queryFn: () => api.groups({ kind: "exact", page: 1, size: 20, sort: "reclaim" }),
    enabled: step === 3,
  });
  const scanTask = useQuery({
    queryKey: ["wizard-scan", activeScanId],
    queryFn: () => api.scan(activeScanId!),
    enabled: step === 2 && activeScanId !== null,
    refetchInterval: 2000,
  });

  // 首次拿到存储卷时默认全选（只做一次，用户手动清空后不再自动填回）
  useEffect(() => {
    if (rootsInitedRef.current || rootList.length === 0) return;
    rootsInitedRef.current = true;
    setRootIDs(rootList.filter((r: Root) => r.enabled).map((r: Root) => r.id));
  }, [rootList]);

  // 已有结果就直接给清单，不再逼用户重新走一遍设置
  useEffect(() => {
    if (initedRef.current || !batches.data) return;
    initedRef.current = true;
    const act = batches.data.items.find((b: ScanBatch) => b.active);
    if (act && act.groups > 0) setStep(3);
  }, [batches.data]);

  useEffect(() => {
    if (keeper.data?.rule) setKeeperRule(keeper.data.rule);
  }, [keeper.data?.rule]);

  // 扫描结束自动进入核对清单
  useEffect(() => {
    if (step !== 2 || !scanTask.data) return;
    if (scanTask.data.status === "succeeded") {
      qc.invalidateQueries({ queryKey: ["batches"] });
      qc.invalidateQueries({ queryKey: ["group-summary"] });
      qc.invalidateQueries({ queryKey: ["wizard-groups"] });
      qc.invalidateQueries({ queryKey: ["groups"] });
      setStep(3);
      setNotice(null);
    }
  }, [scanTask.data, step, qc]);

  const startScan = useMutation({
    mutationFn: async () => {
      // 保留条件按类型持久化，扫描重建分组后由后端自动恢复
      if (keeperRule !== (keeper.data?.rule ?? "")) {
        await api.applyKeeperRule({ kind: "exact", rule: keeperRule });
      }
      return api.createScan({
        name: `一键清理 · ${new Date().toLocaleString("zh-CN", { hour12: false })}`,
        mode: "incremental",
        profile: "fast",
        root_ids: rootIDs,
        extra_paths: extraPaths,
        detect_exact: true,
        detect_similar_image: withSimilarImage,
        detect_similar_video: withSimilarVideo && ffmpegOK,
        defer_verify: verifyMode !== "strict",
        skip_verify: verifyMode === "turbo",
        ack_skip_verify: verifyMode === "turbo",
      });
    },
    onSuccess: (task: ScanTask) => {
      setActiveScanId(task.id);
      setStep(2);
      setNotice(null);
      qc.invalidateQueries({ queryKey: ["scans"] });
      qc.invalidateQueries({ queryKey: ["current-scan"] });
    },
  });

  const doClean = useMutation({
    mutationFn: async () => {
      const task = await api.createCleanTask({
        name: "一键清理 · 完全重复",
        group_kind: "exact",
        execute_mode: "quarantine",
      });
      await api.executeClean(task.id);
      return task.id;
    },
    onSuccess: (id: number) => {
      setCleanTaskId(id);
      setCleanConfirmOpen(false);
      setStep(4);
      qc.invalidateQueries({ queryKey: ["batches"] });
      qc.invalidateQueries({ queryKey: ["group-summary"] });
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
    },
  });

  const switchBatch = useMutation({
    mutationFn: (id: number) => api.switchBatch(id),
    onSuccess: () => {
      setExpanded(new Set());
      qc.invalidateQueries({ queryKey: ["batches"] });
      qc.invalidateQueries({ queryKey: ["group-summary"] });
      qc.invalidateQueries({ queryKey: ["wizard-groups"] });
    },
  });

  const trustAll = useMutation({
    mutationFn: () => api.trustAll(false),
    onSuccess: (res: TrustAllResult) => {
      setTrustConfirmOpen(false);
      setNotice(`已把 ${res.changed} 组「未校验」标记为免校验，清理时不再比对内容。`);
      qc.invalidateQueries({ queryKey: ["batches"] });
      qc.invalidateQueries({ queryKey: ["group-summary"] });
      qc.invalidateQueries({ queryKey: ["wizard-groups"] });
    },
  });

  const exactSum = summary.data?.items.find((x: GroupSummaryItem) => x.kind === "exact");
  const removable = exactSum?.removable ?? activeBatch?.removable ?? 0;
  const reclaimable = exactSum?.bytes ?? activeBatch?.bytes ?? 0;
  const noneSelected = rootIDs.length === 0 && extraPaths.length === 0;
  const risky = verifyMode === "turbo";

  const begin = () => {
    if (noneSelected) return;
    if (risky && !turboAck) {
      setTurboConfirmOpen(true);
      return;
    }
    startScan.mutate();
  };

  const toggleExpand = (id: number) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  return (
    <div className="pb-12">
      <PageHeader
        title="一键清理"
        desc="设定目录与规则 → 找出重复 → 核对清单 → 确认清理，四步都在这一页完成，不用在各个菜单之间来回跳。"
      />

      <div className="space-y-4 px-4 sm:px-5">
        <Card>
          <CardBody className="py-3">
            <StepBar current={step} />
          </CardBody>
        </Card>

        {notice && (
          <div className="flex items-start gap-2 rounded-md border border-primary/25 bg-primary/[0.05] px-3 py-2 text-[12px] text-foreground">
            <Info className="mt-0.5 h-3.5 w-3.5 shrink-0 text-primary" />
            <span className="min-w-0 flex-1">{notice}</span>
            <button onClick={() => setNotice(null)} className="text-muted-foreground hover:text-foreground">
              关闭
            </button>
          </div>
        )}

        {/* ---------------- 第 1 步 ---------------- */}
        {step === 1 && (
          <>
            <Card>
              <CardHeader>
                <CardTitle hint="只在这些目录里找重复，不改动任何文件">① 要检查哪些目录？</CardTitle>
              </CardHeader>
              <CardBody className="space-y-3">
                {roots.isLoading ? (
                  <Loading label="读取存储卷…" className="py-4" />
                ) : rootList.length === 0 ? (
                  <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[12px] text-muted-foreground">
                    还没有注册存储卷。可以直接点下方「浏览目录」挑选，系统会自动登记。
                  </div>
                ) : (
                  <div className="grid gap-2 sm:grid-cols-2">
                    {rootList.map((r: Root) => {
                      const on = rootIDs.includes(r.id);
                      return (
                        <button
                          key={r.id}
                          onClick={() =>
                            setRootIDs((prev) => (on ? prev.filter((x) => x !== r.id) : [...prev, r.id]))
                          }
                          className={cn(
                            "flex items-start gap-2.5 rounded-lg border p-3 text-left transition-colors",
                            on ? "border-primary/50 bg-primary/[0.05]" : "border-border hover:border-primary/30",
                          )}
                        >
                          <span
                            className={cn(
                              "mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded border",
                              on ? "border-primary bg-primary text-primary-foreground" : "border-border",
                            )}
                          >
                            {on && <Check className="h-3 w-3" strokeWidth={3} />}
                          </span>
                          <span className="min-w-0 flex-1">
                            <span className="flex items-center gap-1.5">
                              <HardDrive className="h-3.5 w-3.5 text-muted-foreground" />
                              <span className="truncate text-[13px] font-medium">{r.name}</span>
                              {!r.enabled && <Badge tone="outline">已停用</Badge>}
                            </span>
                            <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground">
                              {r.real_path}
                            </span>
                          </span>
                        </button>
                      );
                    })}
                  </div>
                )}

                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    icon={<FolderPlus className="h-3.5 w-3.5" />}
                    onClick={() => setPickerOpen(true)}
                  >
                    浏览并添加其它目录
                  </Button>
                  {extraPaths.map((p) => (
                    <span
                      key={p}
                      className="inline-flex items-center gap-1.5 rounded-md border border-border bg-muted px-2 py-1 font-mono text-[11px]"
                    >
                      <FolderOpen className="h-3 w-3" />
                      <span className="max-w-[240px] truncate">{p}</span>
                      <button
                        onClick={() => setExtraPaths((prev) => prev.filter((x) => x !== p))}
                        className="text-muted-foreground hover:text-destructive"
                        aria-label="移除目录"
                      >
                        ×
                      </button>
                    </span>
                  ))}
                </div>
              </CardBody>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle hint="这几个选项合起来就是本次的「规则」">② 按什么规则找？</CardTitle>
              </CardHeader>
              <CardBody className="space-y-4">
                <div>
                  <div className="mb-2 text-[12.5px] font-medium">想找什么</div>
                  <div className="grid gap-2 sm:grid-cols-3">
                    <ChoiceCard
                      selected
                      icon={<Copy className="h-4 w-4" />}
                      title="完全重复"
                      desc="内容相同的副本，每组保留 1 份。始终启用。"
                      disabled
                    />
                    <ChoiceCard
                      selected={withSimilarImage}
                      onClick={() => setWithSimilarImage((v) => !v)}
                      icon={<Images className="h-4 w-4" />}
                      title="顺带找相似图片"
                      desc="同图不同尺寸/压缩。结果单独在「相似图片」页查看，不进自动清理清单。"
                    />
                    <ChoiceCard
                      selected={withSimilarVideo && ffmpegOK}
                      onClick={() => ffmpegOK && setWithSimilarVideo((v) => !v)}
                      icon={<Play className="h-4 w-4" />}
                      title="顺带找相似视频"
                      desc={ffmpegOK ? "转码/换封装的同一部片子。同样不进自动清理清单。" : "当前环境未检测到 ffmpeg"}
                      disabled={!ffmpegOK}
                    />
                  </div>
                </div>

                <div>
                  <div className="mb-1.5 flex flex-wrap items-center gap-2">
                    <span className="text-[12.5px] font-medium">每组保留哪一份</span>
                    <span className="text-[11px] text-muted-foreground">清理前会按这个条件重新判定，每组至少留 1 份</span>
                  </div>
                  <Select
                    value={keeperRule}
                    onChange={(e) => setKeeperRule(e.target.value)}
                    className="h-9 w-full text-[13px] sm:max-w-[420px]"
                    aria-label="每组保留哪一份"
                  >
                    <option value="">路径最浅（默认，等同系统原始行为）</option>
                    {defs.map((d) => (
                      <option key={d.id} value={d.id}>{d.label}</option>
                    ))}
                  </Select>
                  {keeperRule && defs.find((d) => d.id === keeperRule) && (
                    <div className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
                      判定口径：{defs.find((d) => d.id === keeperRule)?.criteria}
                    </div>
                  )}
                </div>

                <div>
                  <div className="mb-2 flex flex-wrap items-center gap-2">
                    <span className="text-[12.5px] font-medium">校验严格度</span>
                    <span className="text-[11px] text-muted-foreground">决定「多快」和「多稳」</span>
                  </div>
                  <div className="grid gap-2 lg:grid-cols-3">
                    {VERIFY_MODES.map((m) => (
                      <button
                        key={m.id}
                        onClick={() => {
                          setVerifyMode(m.id);
                          if (m.id !== "turbo") setTurboAck(false);
                        }}
                        className={cn(
                          "rounded-lg border p-3 text-left transition-colors",
                          verifyMode === m.id
                            ? m.id === "turbo"
                              ? "border-destructive/50 bg-destructive/[0.06]"
                              : "border-primary/50 bg-primary/[0.05]"
                            : "border-border hover:border-primary/30",
                        )}
                      >
                        <div className="flex items-center gap-2">
                          <span className={cn("shrink-0", m.id === "turbo" ? "text-destructive" : "text-primary")}>
                            {m.icon}
                          </span>
                          <span className="text-[13px] font-medium">{m.title}</span>
                          <Badge tone={m.tone} className="ml-auto shrink-0">{m.badge}</Badge>
                        </div>
                        <p className="mt-1.5 text-[11.5px] leading-relaxed text-muted-foreground">{m.desc}</p>
                      </button>
                    ))}
                  </div>
                  {risky && (
                    <div className="mt-2 flex items-start gap-2 rounded-md border border-destructive/35 bg-destructive/[0.06] px-3 py-2 text-[11.5px] leading-relaxed text-destructive">
                      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                      <span>
                        该模式下本批分组标为「免校验」，清理时不会与保留副本逐字节比对。
                        同大小但内容不同的文件可能被误删，请只对确定由复制产生的目录使用。
                      </span>
                    </div>
                  )}
                </div>
              </CardBody>
            </Card>

            <Card className="border-primary/30">
              <CardBody className="flex flex-wrap items-center justify-between gap-3 p-4">
                <div className="min-w-0 text-[12px] leading-relaxed text-muted-foreground">
                  将检查 <b className="text-foreground">{rootIDs.length} 个存储卷</b>
                  {extraPaths.length > 0 && <> + <b className="text-foreground">{extraPaths.length} 个目录</b></>}
                  ，按「{VERIFY_MODES.find((m) => m.id === verifyMode)?.title}」查找完全重复。
                  <div className="mt-0.5">扫描只读，不会改动或删除任何文件。</div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  {noneSelected && <span className="text-[11.5px] text-warning">请先选择目录</span>}
                  {startScan.isError && (
                    <span className="max-w-[280px] truncate text-[11.5px] text-destructive">
                      {(startScan.error as Error).message}
                    </span>
                  )}
                  <Button
                    variant="primary"
                    size="md"
                    icon={<ScanLine className="h-4 w-4" />}
                    loading={startScan.isPending}
                    disabled={noneSelected}
                    onClick={begin}
                  >
                    开始查找重复文件 <ArrowRight className="h-3.5 w-3.5" />
                  </Button>
                </div>
              </CardBody>
            </Card>
          </>
        )}

        {/* ---------------- 第 2 步 ---------------- */}
        {step === 2 && (
          <Card>
            <CardHeader action={<Badge tone="info">扫描中</Badge>}>
              <CardTitle hint={`任务 #${activeScanId ?? "—"} · 可以随时离开，结果会留在这一页`}>
                正在查找重复文件
              </CardTitle>
            </CardHeader>
            <CardBody className="space-y-4">
              {scanTask.isLoading ? (
                <Loading label="读取进度…" className="py-8" />
              ) : scanTask.isError ? (
                <ErrorBox message={(scanTask.error as Error).message} onRetry={() => scanTask.refetch()} />
              ) : (
                <>
                  <div className="flex flex-wrap items-center gap-3">
                    <Loader2 className="h-5 w-5 animate-spin text-primary" />
                    <span className="text-[13px] font-medium">
                      {scanTask.data?.phase_label || "准备中"}
                    </span>
                    <span className="text-[11.5px] text-muted-foreground">
                      已扫描 {formatNumber(scanTask.data?.files_seen ?? 0)} 个文件 ·{" "}
                      {formatBytes(scanTask.data?.bytes_scanned ?? 0)}
                    </span>
                  </div>
                  <Progress value={Number(scanTask.data?.progress?.percent ?? 0) || 0} animated />
                  {scanTask.data?.message && (
                    <div className="text-[12px] text-muted-foreground">{scanTask.data.message}</div>
                  )}
                  {scanTask.data?.current_path && (
                    <div className="truncate font-mono text-[10.5px] text-muted-foreground/80" title={scanTask.data.current_path}>
                      {scanTask.data.current_path}
                    </div>
                  )}
                  {["failed", "canceled"].includes(scanTask.data?.status ?? "") && (
                    <div className="rounded-md border border-destructive/30 bg-destructive/8 px-3 py-2 text-[12px] text-destructive">
                      扫描{scanTask.data?.status === "canceled" ? "已取消" : "失败"}：{scanTask.data?.message}
                    </div>
                  )}
                </>
              )}
              <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3">
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<ArrowLeft className="h-3.5 w-3.5" />}
                  onClick={() => setStep(1)}
                >
                  返回修改规则
                </Button>
                {["failed", "canceled"].includes(scanTask.data?.status ?? "") && (
                  <Button size="sm" variant="outline" icon={<RefreshCw className="h-3.5 w-3.5" />} onClick={begin}>
                    重新扫描
                  </Button>
                )}
              </div>
            </CardBody>
          </Card>
        )}

        {/* ---------------- 第 3 步 ---------------- */}
        {step === 3 && (
          <>
            <Card className="border-primary/25">
              <CardBody className="space-y-3 p-4">
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <div>
                    <div className="text-[13px] font-semibold">找到这些重复，先核对一遍清单</div>
                    <div className="mt-0.5 text-[11.5px] text-muted-foreground">
                      每组保留 1 份（下方绿标），其余副本才进入清理清单。
                    </div>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <Select
                      value={activeTaskId || ""}
                      onChange={(e) => switchBatch.mutate(Number(e.target.value))}
                      disabled={switchBatch.isPending || (batches.data?.items.length ?? 0) === 0}
                      className="h-9 min-w-[220px] text-[12px] sm:w-[280px]"
                      aria-label="切换结果批次"
                    >
                      {(batches.data?.items ?? []).map((b: ScanBatch) => (
                        <option key={b.scan_task_id} value={b.scan_task_id}>
                          #{b.scan_task_id} {b.name} · {formatNumber(b.groups)} 组
                        </option>
                      ))}
                    </Select>
                    <Button
                      size="sm"
                      variant="ghost"
                      icon={<ArrowLeft className="h-3.5 w-3.5" />}
                      onClick={() => setStep(1)}
                    >
                      重新设定
                    </Button>
                  </div>
                </div>

                <div className="grid gap-2 sm:grid-cols-3">
                  <MiniStat label="重复分组" value={formatNumber(exactSum?.groups ?? activeBatch?.groups ?? 0)} />
                  <MiniStat label="可删副本" value={formatNumber(removable)} />
                  <MiniStat label="预计释放" value={formatBytes(reclaimable)} />
                </div>

                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-muted-foreground">
                  <span>
                    校验状态：已校验 <b className="text-foreground">{formatNumber(activeBatch?.verified ?? 0)}</b> 组 ·
                    未校验 <b className="text-foreground">{formatNumber(activeBatch?.unverified ?? 0)}</b> 组
                  </span>
                  {(activeBatch?.trusted ?? 0) > 0 && (
                    <span className="text-destructive">
                      免校验 <b>{formatNumber(activeBatch?.trusted ?? 0)}</b> 组（清理时不比对）
                    </span>
                  )}
                  {(activeBatch?.unverified ?? 0) > 0 && (
                    <Button
                      size="sm"
                      variant="outline"
                      className="h-7 text-[11px]"
                      icon={<CheckCheck className="h-3 w-3" />}
                      onClick={() => setTrustConfirmOpen(true)}
                    >
                      一键信任全部未校验分组
                    </Button>
                  )}
                </div>
              </CardBody>
            </Card>

            {groups.isLoading ? (
              <Loading />
            ) : groups.isError ? (
              <ErrorBox message={(groups.error as Error).message} onRetry={() => groups.refetch()} />
            ) : (groups.data?.items.length ?? 0) === 0 ? (
              <Card>
                <EmptyState
                  icon={<Copy className="h-5 w-5" />}
                  title="这一批没有发现完全重复"
                  desc="可以换一批结果看看，或回到第 1 步扩大目录重新扫描。"
                  action={
                    <Button variant="primary" size="sm" onClick={() => setStep(1)}>
                      重新设定并扫描
                    </Button>
                  }
                />
              </Card>
            ) : (
              <div className="space-y-2">
                <div className="flex flex-wrap items-center justify-between gap-2 px-1 text-[11.5px] text-muted-foreground">
                  <span>清单（按可释放空间排序，显示前 {groups.data?.items.length ?? 0} 组）</span>
                  <Link to="/duplicates" className="text-primary hover:underline">
                    去分组页看全部并逐个预览 →
                  </Link>
                </div>
                {(groups.data?.items ?? []).map((g: DupGroup) => (
                  <ReviewRow
                    key={g.id}
                    group={g}
                    expanded={expanded.has(g.id)}
                    onToggle={() => toggleExpand(g.id)}
                  />
                ))}
              </div>
            )}

            <Card className="border-primary/30">
              <CardBody className="flex flex-wrap items-center justify-between gap-3 p-4">
                <div className="min-w-0 text-[12px] leading-relaxed text-muted-foreground">
                  确认后会把 <b className="text-foreground">{formatNumber(removable)}</b> 个副本
                  （<b className="text-foreground">{formatBytes(reclaimable)}</b>）移入隔离区，
                  <b className="text-foreground">不是立即删除</b>，保留期内随时可恢复。
                  <div className="mt-0.5">每组至少保留 1 份；执行前会重新核对保留副本仍然存在。</div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  {doClean.isError && (
                    <span className="max-w-[260px] truncate text-[11.5px] text-destructive">
                      {(doClean.error as Error).message}
                    </span>
                  )}
                  <Button
                    variant="primary"
                    size="md"
                    icon={<Eraser className="h-4 w-4" />}
                    loading={doClean.isPending}
                    disabled={!removable}
                    onClick={() => setCleanConfirmOpen(true)}
                  >
                    清单没问题，开始清理
                  </Button>
                </div>
              </CardBody>
            </Card>
          </>
        )}

        {/* ---------------- 第 4 步 ---------------- */}
        {step === 4 && (
          <Card className="border-success/35">
            <CardHeader action={<Badge tone="success">已开始</Badge>}>
              <CardTitle hint={`清理任务 #${cleanTaskId ?? "—"}`}>清理已提交，后台正在执行</CardTitle>
            </CardHeader>
            <CardBody className="space-y-3">
              <div className="rounded-md border border-border bg-muted/30 px-3 py-2.5 text-[12.5px] leading-relaxed text-muted-foreground">
                文件会先移动到隔离区，保留期内可以随时恢复；执行结果与逐字节复验记录会在「清理任务」页实时显示。
                若某个「未校验」分组与保留副本内容不一致，该副本会被自动跳过。
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Button variant="primary" size="sm" icon={<Trash2 className="h-3.5 w-3.5" />} onClick={() => (window.location.href = "/clean")}>
                  查看执行进度
                </Button>
                <Button variant="outline" size="sm" icon={<ShieldCheck className="h-3.5 w-3.5" />} onClick={() => (window.location.href = "/quarantine")}>
                  打开隔离区
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  icon={<RefreshCw className="h-3.5 w-3.5" />}
                  onClick={() => {
                    setStep(1);
                    setNotice(null);
                    qc.invalidateQueries({ queryKey: ["batches"] });
                    qc.invalidateQueries({ queryKey: ["group-summary"] });
                  }}
                >
                  再扫一次
                </Button>
              </div>
            </CardBody>
          </Card>
        )}
      </div>

      {/* 极速免校验的二次确认 */}
      <ConfirmDialog
        open={turboConfirmOpen}
        onClose={() => setTurboConfirmOpen(false)}
        onConfirm={() => {
          setTurboAck(true);
          setTurboConfirmOpen(false);
          startScan.mutate();
        }}
        loading={startScan.isPending}
        title="确认使用「极速（免校验）」？"
        confirmText="我已了解风险，开始扫描"
        danger
        desc={
          <>
            本批分组<b>不读全文</b>，且<b>执行清理时也不会与保留副本逐字节比对</b>，可以直接批量删除。
            <div className="mt-2 text-destructive">
              风险：只依据文件大小与 4 段采样判定，大小相同、采样段也相同但内容不同的文件会被当成副本删除。
            </div>
            <div className="mt-2">
              建议只用于确定由复制/备份产生的目录，并保留默认的「移入隔离区」以便恢复。
              该选择会写入扫描参数与审计日志，之后可在结果页「撤销免校验」。
            </div>
          </>
        }
      />

      {/* 一键信任未校验分组 */}
      <ConfirmDialog
        open={trustConfirmOpen}
        onClose={() => setTrustConfirmOpen(false)}
        onConfirm={() => trustAll.mutate()}
        loading={trustAll.isPending}
        title={`把 ${formatNumber(activeBatch?.unverified ?? 0)} 组「未校验」标记为免校验？`}
        confirmText="我已了解风险，全部标记"
        danger
        desc={
          <>
            这不会重新全文扫描，只是跳过清理前的逐字节比对，让这些分组能直接批量删除。
            <div className="mt-2 text-destructive">
              风险：同大小但内容不同的文件不会再被拦下。请确认这些目录确实由复制产生。
            </div>
            <div className="mt-2">只作用于当前批次；可在「完全重复」页点「撤销免校验」恢复比对。</div>
          </>
        }
      />

      {/* 清理前的最后确认 */}
      <ConfirmDialog
        open={cleanConfirmOpen}
        onClose={() => setCleanConfirmOpen(false)}
        onConfirm={() => doClean.mutate()}
        loading={doClean.isPending}
        title="确认按这份清单清理？"
        confirmText="确认，开始清理"
        danger
        desc={
          <>
            将把全部完全重复分组里的非保留副本（{formatNumber(removable)} 个、
            {formatBytes(reclaimable)}）一次性纳入清理任务并立即执行，每组保留 1 份。
            <div className="mt-2">文件进入隔离区、保留期内可恢复；「未校验」分组会先与保留副本逐字节比对，不一致的会被跳过。</div>
            {(activeBatch?.trusted ?? 0) > 0 && (
              <div className="mt-2 text-destructive">
                当前批次有 {formatNumber(activeBatch?.trusted ?? 0)} 组标为「免校验」，这些组清理时不比对内容，
                存在同大小不同内容被误删的风险。
              </div>
            )}
            {keeperRule && (
              <div className="mt-2">
                执行前会按「{defs.find((d) => d.id === keeperRule)?.label ?? keeperRule}」逐组重算保留文件。
              </div>
            )}
            {doClean.isError && (
              <div className="mt-2 text-destructive">{(doClean.error as Error).message}</div>
            )}
          </>
        }
      />

      <DirectoryPicker
        open={pickerOpen}
        onClose={() => setPickerOpen(false)}
        title="选择要检查的目录"
        desc="从已注册存储卷或挂载的媒体目录中选择，可多次添加"
        pickLabel="加入检查范围"
        onPick={(p) => {
          setExtraPaths((prev) => (prev.includes(p) ? prev : [...prev, p]));
          setPickerOpen(false);
        }}
      />
    </div>
  );
}

function StepBar({ current }: { current: Step }) {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-2">
      {STEPS.map((s, i) => {
        const n = i + 1;
        const active = n === current;
        const done = n < current;
        return (
          <div key={s} className="flex items-center gap-2">
            <span
              className={cn(
                "flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold",
                active
                  ? "bg-primary text-primary-foreground"
                  : done
                    ? "bg-success/15 text-success"
                    : "bg-muted text-muted-foreground",
              )}
            >
              {done ? <Check className="h-3.5 w-3.5" strokeWidth={3} /> : n}
            </span>
            <span className={cn("whitespace-nowrap text-[12px]", active ? "font-semibold text-foreground" : "text-muted-foreground")}>
              {s}
            </span>
            {i < STEPS.length - 1 && <span className="mx-1 h-px w-5 bg-border sm:w-9" />}
          </div>
        );
      })}
    </div>
  );
}

function ChoiceCard({
  selected,
  onClick,
  icon,
  title,
  desc,
  disabled,
}: {
  selected: boolean;
  onClick?: () => void;
  icon: React.ReactNode;
  title: string;
  desc: string;
  disabled?: boolean;
}) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className={cn(
        "rounded-lg border p-3 text-left transition-colors",
        selected ? "border-primary/50 bg-primary/[0.05]" : "border-border hover:border-primary/30",
        disabled && "cursor-default",
      )}
    >
      <div className="flex items-center gap-2">
        <span className={cn(selected ? "text-primary" : "text-muted-foreground")}>{icon}</span>
        <span className="text-[12.5px] font-medium">{title}</span>
        {selected && <Check className="ml-auto h-3.5 w-3.5 text-primary" strokeWidth={3} />}
      </div>
      <p className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">{desc}</p>
    </button>
  );
}

function MiniStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-border bg-muted/25 px-3 py-2.5">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-[17px] font-semibold tabular-nums">{value}</div>
    </div>
  );
}

function ReviewRow({
  group,
  expanded,
  onToggle,
}: {
  group: DupGroup;
  expanded: boolean;
  onToggle: () => void;
}) {
  const keeper = group.members.find((m) => m.is_keeper);
  const deletable = group.members.filter((m) => !m.is_keeper);
  return (
    <Card className="overflow-hidden">
      <div className="flex flex-wrap items-center gap-2 px-3 py-2.5">
        <span className="mono shrink-0 text-[11px] text-muted-foreground">#{group.id}</span>
        <Badge tone="info" className="shrink-0">{group.member_count} 个文件</Badge>
        <Badge tone="warn" className="shrink-0">可释放 {formatBytes(group.reclaimable_bytes)}</Badge>
        {group.evidence === "sampled" && <Badge tone="warn" className="shrink-0">未校验</Badge>}
        {group.evidence === "trusted" && <Badge tone="danger" className="shrink-0">免校验</Badge>}
        {group.evidence === "exact" && <Badge tone="success" className="shrink-0">已校验</Badge>}
        <div className="min-w-0 flex-1 truncate text-[11.5px] text-muted-foreground" title={keeper?.path}>
          {keeper ? `保留：${keeper.name}` : "尚未指定保留文件"}
        </div>
        <button
          onClick={onToggle}
          className="flex shrink-0 items-center gap-1 text-[11.5px] text-primary hover:underline"
        >
          {expanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
          {expanded ? "收起清单" : `查看 ${deletable.length} 个待删副本`}
        </button>
      </div>

      {!expanded && keeper && (
        <div className="flex items-center gap-2 border-t border-border/60 bg-success/[0.04] px-3 py-1.5 text-[11px] text-muted-foreground">
          <ShieldCheck className="h-3.5 w-3.5 shrink-0 text-success" />
          <span className="truncate" title={keeper.path}>{keeper.path}</span>
        </div>
      )}

      {expanded && (
        <div className="divide-y divide-border/60 border-t border-border/60">
          {keeper && (
            <div className="flex items-center gap-2 bg-success/[0.04] px-3 py-2">
              <ShieldCheck className="h-3.5 w-3.5 shrink-0 text-success" />
              <span className="shrink-0 text-[11px] font-medium text-success">保留</span>
              <span className="min-w-0 flex-1 truncate text-[11.5px]" title={keeper.path}>{keeper.path}</span>
              <span className="shrink-0 tabular-nums text-[11px] text-muted-foreground">{formatBytes(keeper.size)}</span>
            </div>
          )}
          {deletable.map((m) => (
            <div key={m.file_id} className="flex items-center gap-2 px-3 py-2">
              <span className="h-9 w-12 shrink-0 overflow-hidden rounded border border-border bg-muted">
                <Thumb fileId={m.file_id} kind={m.media_kind} width={96} className="h-full w-full" />
              </span>
              <Trash2 className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-[12px]">{m.name}</div>
                <div className="truncate font-mono text-[10.5px] text-muted-foreground" title={m.path}>{m.path}</div>
              </div>
              <div className="shrink-0 text-right text-[10.5px] text-muted-foreground">
                <div className="tabular-nums">{formatBytes(m.size)}</div>
                <div className="tabular-nums">{m.mtime_ns ? formatDate(m.mtime_ns / 1e6, false) : ""}</div>
              </div>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}
