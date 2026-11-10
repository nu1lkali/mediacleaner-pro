import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  Download,
  Eraser,
  FileSpreadsheet,
  Info,
  Loader2,
  Pause,
  Play,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";
import { api } from "../services/api";
import type { CleanItem, CleanTask } from "../services/types";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Checkbox, Field, Input, Select, Switch } from "../components/ui/field";
import { ConfirmDialog, Dialog } from "../components/ui/dialog";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui/empty";
import { Progress } from "../components/ui/progress";
import { cn, formatBytes, formatDate, formatNumber, kindLabel, statusText, statusTone } from "../lib/utils";

export default function CleanPage() {
  const qc = useQueryClient();
  const tasks = useQuery({ queryKey: ["clean-tasks"], queryFn: () => api.cleanTasks(30, 0), refetchInterval: 5000 });
  const rules = useQuery({ queryKey: ["rules"], queryFn: api.rules });
  const [activeId, setActiveId] = useState<number | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [confirmExec, setConfirmExec] = useState<CleanTask | null>(null);
  const [skipVerify, setSkipVerify] = useState(false);
  const [exportOpen, setExportOpen] = useState<CleanTask | null>(null);

  const list = tasks.data?.items ?? [];
  const active = list.find((t) => t.id === activeId) ?? null;

  const create = useMutation({
    mutationFn: (body: { name?: string; rule_ids?: number[]; execute_mode?: "quarantine" | "permanent" }) =>
      api.createCleanTask(body),
    onSuccess: (t) => {
      setCreateOpen(false);
      setActiveId(t.id);
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
    },
  });

  const execute = useMutation({
    mutationFn: ({ id, skip }: { id: number; skip: boolean }) => api.executeClean(id, skip),
    onSuccess: () => {
      setConfirmExec(null);
      setSkipVerify(false);
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
    },
  });

  const cancel = useMutation({
    mutationFn: (id: number) => api.cancelClean(id),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
      qc.invalidateQueries({ queryKey: ["clean-task", id] });
      qc.invalidateQueries({ queryKey: ["clean-items", id] });
    },
  });

  // 暂停/继续：只改任务状态，执行循环会在下一个文件前挂起/接着做，
  // 已完成项与隔离区记录都保留（想放弃请用「取消执行」）。
  const pause = useMutation({
    mutationFn: (id: number) => api.pauseClean(id),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
      qc.invalidateQueries({ queryKey: ["clean-task", id] });
    },
  });

  const resume = useMutation({
    mutationFn: (id: number) => api.resumeClean(id),
    onSuccess: (_d, id) => {
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
      qc.invalidateQueries({ queryKey: ["clean-task", id] });
    },
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.deleteCleanTask(id),
    onSuccess: (_d, id) => {
      if (activeId === id) setActiveId(null);
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
    },
  });

  return (
    <div className="pb-10">
      <PageHeader
        title="清理任务"
        desc="规则求值 → 生成待删清单 → 人工确认 → 移入隔离区。执行前不会动任何文件。"
        actions={
          <>
            <Button
              variant="ghost"
              size="sm"
              icon={<RefreshCw className="h-3.5 w-3.5" />}
              onClick={() => qc.invalidateQueries({ queryKey: ["clean-tasks"] })}
            >
              刷新
            </Button>
            <Button variant="primary" size="sm" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setCreateOpen(true)}>
              新建清理任务
            </Button>
          </>
        }
      />

      <div className="space-y-4 px-4 sm:px-5">
        <div className="grid gap-4 xl:grid-cols-[380px_1fr]">
          {/* 任务列表 */}
          <Card className="h-fit">
            <CardHeader>
              <CardTitle hint={`${list.length} 个任务`}>任务列表</CardTitle>
            </CardHeader>
            {tasks.isLoading ? (
              <Loading />
            ) : list.length === 0 ? (
              <EmptyState
                icon={<Eraser className="h-5 w-5" />}
                title="还没有清理任务"
                desc="从规则生成一份待删清单，确认无误后再执行。"
                className="py-10"
                action={
                  <Button size="sm" variant="primary" onClick={() => setCreateOpen(true)}>
                    新建任务
                  </Button>
                }
              />
            ) : (
              <div className="max-h-[600px] divide-y divide-border/70 overflow-y-auto">
                {list.map((t) => {
                  const stats = t.stats as { selected?: number; selected_bytes?: number; failed?: number };
                  return (
                    <button
                      key={t.id}
                      onClick={() => setActiveId(t.id)}
                      className={cn(
                        "w-full px-3 py-2.5 text-left transition-colors hover:bg-muted/60",
                        activeId === t.id && "bg-primary/[0.05]",
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <Badge tone={statusTone(t.status)}>{statusText[t.status] ?? t.status}</Badge>
                        <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium">{t.name}</span>
                      </div>
                      <div className="mt-1 flex items-center gap-2 text-[11px] text-muted-foreground">
                        <span className="tabular-nums">#{t.id}</span>
                        <span>{t.execute_mode === "permanent" ? "永久删除" : "移入隔离区"}</span>
                        {typeof stats.selected === "number" && (
                          <span>· {formatNumber(stats.selected)} 项 / {formatBytes(stats.selected_bytes ?? 0)}</span>
                        )}
                      </div>
                      <div className="mt-0.5 text-[10.5px] text-muted-foreground/80">{formatDate(t.created_at)}</div>
                    </button>
                  );
                })}
              </div>
            )}
          </Card>

          {/* 任务详情 */}
          <div className="min-w-0">
            {!active ? (
              <Card>
                <EmptyState
                  icon={<Info className="h-5 w-5" />}
                  title="选择一个任务查看详情"
                  desc="左侧列表点选任务，可以逐项确认要删除的文件、执行或取消。"
                  className="py-16"
                />
              </Card>
            ) : (
              <TaskDetail
                task={active}
                onExecute={() => setConfirmExec(active)}
                onCancel={() => cancel.mutate(active.id)}
                onPause={() => pause.mutate(active.id)}
                onResume={() => resume.mutate(active.id)}
                onDelete={() => remove.mutate(active.id)}
                onExport={() => setExportOpen(active)}
                cancelling={cancel.isPending}
                pausing={pause.isPending || resume.isPending}
                actionError={
                  (cancel.isError && (cancel.error as Error).message) ||
                  (pause.isError && (pause.error as Error).message) ||
                  (resume.isError && (resume.error as Error).message) ||
                  ""
                }
              />
            )}
          </div>
        </div>
      </div>

      {createOpen && (
        <CreateTaskDialog
          rules={(rules.data?.items ?? []).filter((r) => r.enabled)}
          onClose={() => setCreateOpen(false)}
          onSubmit={(body) => create.mutate(body)}
          loading={create.isPending}
          error={create.isError ? (create.error as Error).message : undefined}
        />
      )}

      <ConfirmDialog
        open={!!confirmExec}
        onClose={() => setConfirmExec(null)}
        onConfirm={() => confirmExec && execute.mutate({ id: confirmExec.id, skip: skipVerify })}
        loading={execute.isPending}
        title={confirmExec?.execute_mode === "permanent" ? "永久删除这些文件？" : "把这些文件移入隔离区？"}
        confirmText={confirmExec?.execute_mode === "permanent" ? "永久删除" : "移入隔离区"}
        danger={confirmExec?.execute_mode === "permanent"}
        desc={
          <>
            任务「{confirmExec?.name}」将处理列表中已勾选的文件。执行时会再次校验文件修改时间，
            若文件已被改动则自动跳过，避免误删新内容。
            {confirmExec?.execute_mode === "permanent" ? (
              <div className="mt-2 font-medium text-destructive">
                当前为永久删除模式，操作不可撤销，也不进入隔离区。
              </div>
            ) : (
              <div className="mt-2 text-muted-foreground">文件可在隔离区保留期内随时恢复。</div>
            )}
          </>
        }
      >
        <div className="mt-3 rounded-lg border bg-muted/40 p-3">
          <Switch
            checked={skipVerify}
            onChange={setSkipVerify}
            label="跳过逐字节校验，直接执行"
            hint="仅对「未校验」分组生效：默认会与保留副本比对全文，不一致就拒绝删除。跳过更快但有误删风险。"
          />
        </div>
      </ConfirmDialog>

      {exportOpen && <ExportDialog task={exportOpen} onClose={() => setExportOpen(null)} />}
    </div>
  );
}

function TaskDetail({
  task,
  onExecute,
  onCancel,
  onPause,
  onResume,
  onDelete,
  onExport,
  cancelling,
  pausing,
  actionError,
}: {
  task: CleanTask;
  onExecute: () => void;
  onCancel: () => void;
  onPause: () => void;
  onResume: () => void;
  onDelete: () => void;
  onExport: () => void;
  cancelling: boolean;
  pausing: boolean;
  actionError: string;
}) {
  const qc = useQueryClient();
  // 运行中/已暂停的任务通过详情接口单独拉取，进度与状态比列表轮询更及时
  const live = useQuery({
    queryKey: ["clean-task", task.id],
    queryFn: () => api.cleanTask(task.id),
    refetchInterval: task.status === "running" || task.status === "paused" ? 2000 : false,
  });
  const cur = live.data ?? task;
  const busy = cur.status === "running" || cur.status === "paused";

  // 明细按「执行顺序」分页取：默认只拉待处理的前 60 项（第一条就是正在执行的文件），
  // 滚到底自动续拉。以前一次返回几千行，浏览器会直接卡死。
  const [pendingLimit, setPendingLimit] = useState(PAGE_SIZE);
  const [doneLimit, setDoneLimit] = useState(PAGE_SIZE);
  const [showDone, setShowDone] = useState(false);

  const pending = useQuery({
    queryKey: ["clean-items", task.id, "pending", pendingLimit],
    queryFn: () => api.cleanItems(task.id, { filter: "pending", limit: pendingLimit }),
    refetchInterval: busy ? 2000 : false,
  });
  const counts = pending.data;

  const done = useQuery({
    queryKey: ["clean-items", task.id, "done", doneLimit],
    queryFn: () => api.cleanItems(task.id, { filter: "done", limit: doneLimit }),
    enabled: showDone && (counts?.done ?? 0) > 0,
    refetchInterval: busy ? 3000 : false,
  });

  const invalidateItems = () => qc.invalidateQueries({ queryKey: ["clean-items", task.id] });
  const select = useMutation({
    mutationFn: ({ id, selected }: { id: number; selected: boolean }) => api.selectItem(id, selected),
    onSuccess: invalidateItems,
  });

  const running = cur.status === "running";
  const paused = cur.status === "paused";
  const editable = cur.status === "draft";

  // 取消是"请求发出 → 循环收尾"两步，中间可能隔着一个大文件的隔离。
  // 记一个本地标记，让按钮立刻给出"正在收尾"的反馈，而不是看起来没反应。
  const [cancelRequested, setCancelRequested] = useState(false);
  useEffect(() => {
    if (!busy || actionError) setCancelRequested(false);
  }, [busy, actionError]);
  const requestCancel = () => {
    setCancelRequested(true);
    onCancel();
  };
  // 进度用真实计数算：待执行 + 已完成 + 失败 + 跳过 = 本次要处理的总量
  const scope = (counts?.pending ?? 0) + (counts?.done ?? 0) + (counts?.failed ?? 0) + (counts?.skipped ?? 0);
  const settled = (counts?.done ?? 0) + (counts?.failed ?? 0) + (counts?.skipped ?? 0);
  const percent = scope > 0 ? (settled / scope) * 100 : 0;

  return (
    <Card>
      <CardHeader
        action={
          <div className="flex flex-wrap items-center gap-1.5">
            <Button size="sm" variant="ghost" icon={<Download className="h-3.5 w-3.5" />} onClick={onExport}>
              导出
            </Button>
            {editable && (
              <>
                <Button size="sm" variant="ghost" className="text-destructive" onClick={onDelete}>
                  删除任务
                </Button>
                <Button size="sm" variant="primary" icon={<Play className="h-3.5 w-3.5" />} onClick={onExecute}>
                  执行清理
                </Button>
              </>
            )}
            {running && (
              <>
                <Button
                  size="sm"
                  variant="outline"
                  icon={<Pause className="h-3.5 w-3.5" />}
                  loading={pausing}
                  onClick={onPause}
                >
                  暂停
                </Button>
                <Button size="sm" variant="outline" loading={cancelling || cancelRequested} onClick={requestCancel}>
                  取消执行
                </Button>
              </>
            )}
            {paused && (
              <>
                <Button
                  size="sm"
                  variant="primary"
                  icon={<Play className="h-3.5 w-3.5" />}
                  loading={pausing}
                  onClick={onResume}
                >
                  继续
                </Button>
                <Button size="sm" variant="outline" loading={cancelling || cancelRequested} onClick={requestCancel}>
                  取消执行
                </Button>
              </>
            )}
          </div>
        }
      >
        <CardTitle hint={`#${cur.id} · ${cur.execute_mode === "permanent" ? "永久删除" : "移入隔离区"}`}>
          {cur.name}
        </CardTitle>
      </CardHeader>

      <CardBody className="space-y-3">
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Metric label="待执行" value={formatNumber(counts?.pending ?? 0)} tone="primary" />
          <Metric label="已处理" value={formatNumber(counts?.done ?? 0)} tone="success" />
          <Metric label="可释放" value={formatBytes(counts?.selected_bytes ?? 0)} tone="success" />
          <Metric
            label="状态"
            value={statusText[cur.status] ?? cur.status}
            tone={cur.status === "failed" ? "danger" : cur.status === "succeeded" ? "success" : "default"}
          />
        </div>

        {(running || paused) && (
          <div>
            <Progress value={percent} animated={running} />
            <div className="mt-1 flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
              {paused && <Pause className="h-3 w-3 shrink-0" />}
              {paused
                ? `${cur.message || "任务已暂停"} 已完成的项与隔离区记录都保留，点「继续」从下一个文件接着执行。`
                : `${formatNumber(settled)} / ${formatNumber(scope)} · ${cur.message || "正在处理…"}`}
            </div>
          </div>
        )}
        {cancelRequested && (
          <div className="flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
            <Loader2 className="h-3 w-3 shrink-0 animate-spin" />
            取消请求已发出，正在收尾当前文件（大文件隔离需要几秒），已处理的不会回滚。
          </div>
        )}
        {actionError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/8 px-3 py-2 text-[12px] text-destructive">
            {actionError}
          </div>
        )}
        {cur.message && !running && !paused && (
          <div
            className={cn(
              "flex items-start gap-2 rounded-md px-3 py-2 text-[12px]",
              cur.status === "failed"
                ? "border border-destructive/30 bg-destructive/8 text-destructive"
                : cur.status === "succeeded"
                  ? "border border-success/30 bg-success/8 text-success"
                  : "border border-border bg-muted/40 text-muted-foreground",
            )}
          >
            {cur.status === "failed" ? (
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            ) : cur.status === "succeeded" ? (
              <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            ) : (
              <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            )}
            {cur.message}
          </div>
        )}

        {pending.isLoading ? (
          <Loading />
        ) : pending.error ? (
          <ErrorBox message={(pending.error as Error).message} />
        ) : !counts || counts.total === 0 ? (
          <EmptyState icon={<ShieldCheck className="h-5 w-5" />} title="清单为空" desc="规则没有命中任何文件，可能已清理过或条件过严。" className="py-10" />
        ) : (
          <>
            <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
              <div className="text-[11.5px] text-muted-foreground">
                待执行 <span className="text-foreground">{formatNumber(counts.pending)}</span> 项
                <span className="mx-1.5 opacity-40">|</span>
                已处理 <span className="text-foreground">{formatNumber(counts.done)}</span> 项
                {counts.failed > 0 && <span className="text-destructive"> · 失败 {formatNumber(counts.failed)}</span>}
                {counts.skipped > 0 && <span> · 跳过 {formatNumber(counts.skipped)}</span>}
              </div>
              <div className="text-[11px] text-muted-foreground/80">按执行顺序排列，第一条就是正在处理的文件</div>
            </div>

            {counts.pending > 0 ? (
              <>
                <ItemTable
                  items={pending.data?.items ?? []}
                  editable={editable}
                  onToggle={(id, v) => select.mutate({ id, selected: v })}
                />
                <MoreBar
                  more={Math.max(0, counts.pending - (pending.data?.items.length ?? 0))}
                  loading={pending.isFetching && !pending.isLoading}
                  onClick={() => setPendingLimit((n) => n + PAGE_SIZE)}
                />
              </>
            ) : (
              <div className="rounded-md border border-border bg-muted/30 px-3 py-2 text-[11.5px] text-muted-foreground">
                没有待处理项，全部已处理或已取消勾选。
              </div>
            )}

            {counts.done > 0 && (
              <div className="rounded-md border border-border">
                <button
                  onClick={() => setShowDone((v) => !v)}
                  className="flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-[12px] hover:bg-muted/50"
                >
                  <span className="flex min-w-0 items-center gap-1.5">
                    <ChevronDown
                      className={cn("h-3.5 w-3.5 shrink-0 transition-transform", !showDone && "-rotate-90")}
                    />
                    <span className="truncate">已处理 {formatNumber(counts.done)} 项（最近处理的在前）</span>
                  </span>
                  <span className="shrink-0 text-[11px] text-muted-foreground">{showDone ? "收起" : "展开"}</span>
                </button>
                {showDone && (
                  <div className="space-y-2 border-t border-border p-2">
                    {done.isLoading ? (
                      <Loading />
                    ) : (
                      <ItemTable items={done.data?.items ?? []} editable={false} onToggle={() => undefined} />
                    )}
                    <MoreBar
                      more={Math.max(0, counts.done - (done.data?.items.length ?? 0))}
                      loading={done.isFetching && !done.isLoading}
                      onClick={() => setDoneLimit((n) => n + PAGE_SIZE)}
                    />
                  </div>
                )}
              </div>
            )}
          </>
        )}

        {editable && (
          <div className="flex items-start gap-2 rounded-md border border-border bg-muted/40 px-3 py-2 text-[11.5px] text-muted-foreground">
            <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            取消勾选即保留该文件。执行前最后一次确认时，系统会重新校验文件是否被修改。
          </div>
        )}
      </CardBody>
    </Card>
  );
}

/** 明细每页条数：一屏多一点，既能看出趋势，又不会把主线程压死。 */
const PAGE_SIZE = 60;

function ItemTable({
  items,
  editable,
  onToggle,
}: {
  items: CleanItem[];
  editable: boolean;
  onToggle: (id: number, v: boolean) => void;
}) {
  return (
    <div className="max-h-[460px] overflow-auto rounded-md border border-border">
      {/* min-w 保证窄屏下列不被压扁成重叠；容器 overflow-auto 提供横向滚动 */}
      <table className="table min-w-[680px]">
        <thead className="sticky top-0 z-10 bg-card">
          <tr>
            <th className="w-9" />
            <th>文件</th>
            <th className="w-[64px]">类型</th>
            <th className="w-[84px]">大小</th>
            <th className="w-[130px]">保留版本</th>
            <th className="w-[96px]">结果</th>
          </tr>
        </thead>
        <tbody>
          {items.map((it) => (
            <ItemRow
              key={it.id}
              item={it}
              editable={editable}
              onToggle={(v) => onToggle(it.id, v)}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** 懒加载尾巴：滚到可见范围就自动续拉一页，也可以手动点。 */
function MoreBar({
  more,
  loading,
  onClick,
}: {
  more: number;
  loading: boolean;
  onClick: () => void;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const cb = useRef(onClick);
  cb.current = onClick;
  useEffect(() => {
    const el = ref.current;
    if (!el || more <= 0) return;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) cb.current();
      },
      { rootMargin: "200px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [more]);
  if (more <= 0) return null;
  return (
    <div ref={ref} className="flex items-center justify-center py-1.5">
      <Button size="sm" variant="ghost" loading={loading} onClick={onClick}>
        加载更多（还有 {formatNumber(more)} 项）
      </Button>
    </div>
  );
}

function ItemRow({
  item,
  editable,
  onToggle,
}: {
  item: CleanItem;
  editable: boolean;
  onToggle: (v: boolean) => void;
}) {
  const [open, setOpen] = useState(false);
  const done = item.action && item.action !== "pending";
  return (
    <>
      <tr className={cn(!item.selected && "opacity-55", item.error && "bg-destructive/[0.04]")}>
        <td>
          <Checkbox
            checked={item.selected}
            onChange={onToggle}
            className={!editable || done ? "pointer-events-none opacity-50" : undefined}
          />
        </td>
        <td className="max-w-[320px]">
          <button onClick={() => setOpen((v) => !v)} className="block w-full text-left">
            <div className="truncate text-[12px] font-medium" title={item.path}>
              {item.name}
            </div>
            <div className="truncate text-[10.5px] text-muted-foreground" title={item.path}>
              {item.path}
            </div>
          </button>
        </td>
        <td className="text-[11.5px]">{kindLabel(item.media_kind)}</td>
        <td className="text-[11.5px] tabular-nums">{formatBytes(item.size)}</td>
        <td className="max-w-[130px] truncate text-[11px] text-muted-foreground" title={item.keeper_path}>
          {item.keeper_path ? item.keeper_path.split(/[\\/]/).pop() : "—"}
        </td>
        <td>
          {item.error ? (
            <Badge tone="danger">失败</Badge>
          ) : item.action === "quarantined" ? (
            <Badge tone="success">已隔离</Badge>
          ) : item.action === "deleted" ? (
            <Badge tone="success">已删除</Badge>
          ) : item.action === "skipped" ? (
            <Badge tone="warn">已跳过</Badge>
          ) : (
            <Badge tone="outline">待处理</Badge>
          )}
        </td>
      </tr>
      {open && (
        <tr>
          <td />
          <td colSpan={5} className="bg-muted/30">
            <div className="grid gap-x-6 gap-y-1 py-1 text-[11.5px] sm:grid-cols-2">
              <div className="text-muted-foreground">
                原因：<span className="text-foreground">{item.reason || "—"}</span>
              </div>
              <div className="text-muted-foreground">
                命中规则：<span className="text-foreground">{item.hit_rules || "—"}</span>
              </div>
              {item.assoc_of ? (
                <div className="text-muted-foreground">
                  关联主文件 ID：<span className="mono">{item.assoc_of}</span>
                </div>
              ) : null}
              {item.error ? <div className="text-destructive sm:col-span-2">{item.error}</div> : null}
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

function Metric({
  label,
  value,
  tone = "default",
}: {
  label: string;
  value: string;
  tone?: "default" | "primary" | "success" | "danger";
}) {
  const color = {
    default: "text-foreground",
    primary: "text-primary",
    success: "text-success",
    danger: "text-destructive",
  }[tone];
  return (
    <div className="rounded-md border border-border px-3 py-2">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className={cn("mt-0.5 text-[15px] font-semibold tabular-nums", color)}>{value}</div>
    </div>
  );
}

function CreateTaskDialog({
  rules,
  onClose,
  onSubmit,
  loading,
  error,
}: {
  rules: { id: number; name: string; match: { group_kinds: string[] } }[];
  onClose: () => void;
  onSubmit: (body: { name?: string; rule_ids?: number[]; execute_mode?: "quarantine" | "permanent" }) => void;
  loading: boolean;
  error?: string;
}) {
  const [name, setName] = useState("");
  const [picked, setPicked] = useState<number[]>([]);
  const [permanent, setPermanent] = useState(false);

  return (
    <Dialog
      open
      onClose={onClose}
      title="新建清理任务"
      desc="选择规则生成待删清单；清单可逐项调整后再执行"
      footer={
        <>
          {error && <span className="mr-auto text-[12px] text-destructive">{error}</span>}
          <Button size="sm" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button
            size="sm"
            variant="primary"
            loading={loading}
            onClick={() => onSubmit({ name: name.trim() || undefined, rule_ids: picked, execute_mode: permanent ? "permanent" : "quarantine" })}
          >
            生成清单
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label="任务名称" hint="留空自动生成">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例：清理 2023 年前的重复剧集" />
        </Field>

        <div>
          <div className="label">应用规则（不选 = 全部启用规则）</div>
          <div className="space-y-1 rounded-md border border-border p-1.5">
            {rules.length === 0 ? (
              <div className="px-1 py-1 text-[12px] text-muted-foreground">没有启用的规则，请先到「清理规则」启用。</div>
            ) : (
              rules.map((r) => (
                <label key={r.id} className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 hover:bg-muted/60">
                  <Checkbox
                    checked={picked.includes(r.id)}
                    onChange={(v) => setPicked((p) => (v ? [...p, r.id] : p.filter((x) => x !== r.id)))}
                    label={r.name}
                  />
                </label>
              ))
            )}
          </div>
        </div>

        <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5">
          <Switch
            checked={permanent}
            onChange={setPermanent}
            label="直接永久删除（不进入隔离区）"
            hint="默认关闭。开启后删除不可恢复，建议仅在确认清单无误后使用。"
          />
        </div>
      </div>
    </Dialog>
  );
}

function ExportDialog({ task, onClose }: { task: CleanTask; onClose: () => void }) {
  const [format, setFormat] = useState<"csv" | "xlsx" | "json" | "html">("xlsx");
  const [result, setResult] = useState<{ file: string; rows: number; download_url: string } | null>(null);

  const run = useMutation({
    mutationFn: () => api.exportData({ kind: "clean", format, clean_task_id: task.id }),
    onSuccess: (r) => setResult(r),
  });

  return (
    <Dialog
      open
      onClose={onClose}
      title="导出清理清单"
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
