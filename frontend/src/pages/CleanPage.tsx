import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  CheckCircle2,
  Download,
  Eraser,
  FileSpreadsheet,
  Info,
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
    mutationFn: (id: number) => api.executeClean(id),
    onSuccess: () => {
      setConfirmExec(null);
      qc.invalidateQueries({ queryKey: ["clean-tasks"] });
    },
  });

  const cancel = useMutation({
    mutationFn: (id: number) => api.cancelClean(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["clean-tasks"] }),
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
                onDelete={() => remove.mutate(active.id)}
                onExport={() => setExportOpen(active)}
                cancelling={cancel.isPending}
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
        onConfirm={() => confirmExec && execute.mutate(confirmExec.id)}
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
      />

      {exportOpen && <ExportDialog task={exportOpen} onClose={() => setExportOpen(null)} />}
    </div>
  );
}

function TaskDetail({
  task,
  onExecute,
  onCancel,
  onDelete,
  onExport,
  cancelling,
}: {
  task: CleanTask;
  onExecute: () => void;
  onCancel: () => void;
  onDelete: () => void;
  onExport: () => void;
  cancelling: boolean;
}) {
  const qc = useQueryClient();
  // 运行中的任务通过详情接口单独拉取，进度与状态比列表轮询更及时
  const live = useQuery({
    queryKey: ["clean-task", task.id],
    queryFn: () => api.cleanTask(task.id),
    refetchInterval: task.status === "running" ? 2000 : false,
  });
  const cur = live.data ?? task;

  const items = useQuery({
    queryKey: ["clean-items", cur.id],
    queryFn: () => api.cleanItems(cur.id),
    refetchInterval: cur.status === "running" ? 2000 : false,
  });

  const select = useMutation({
    mutationFn: ({ id, selected }: { id: number; selected: boolean }) => api.selectItem(id, selected),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["clean-items", cur.id] }),
  });

  const stats = cur.stats as Record<string, number | string | undefined>;
  const data = items.data;
  const running = cur.status === "running";
  const editable = cur.status === "draft";

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
              <Button size="sm" variant="outline" loading={cancelling} onClick={onCancel}>
                取消执行
              </Button>
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
          <Metric label="待处理" value={formatNumber(data?.total ?? Number(stats.selected) ?? 0)} />
          <Metric label="已勾选" value={formatNumber(data?.selected ?? 0)} tone="primary" />
          <Metric label="可释放" value={formatBytes(data?.selected_bytes ?? 0)} tone="success" />
          <Metric
            label="状态"
            value={statusText[cur.status] ?? cur.status}
            tone={cur.status === "failed" ? "danger" : cur.status === "succeeded" ? "success" : "default"}
          />
        </div>

        {cur.status === "running" && (
          <div>
            <Progress value={Number(stats.percent ?? 0)} animated />
            <div className="mt-1 text-[11.5px] text-muted-foreground">
              {cur.message || "正在处理…"}
            </div>
          </div>
        )}
        {cur.message && cur.status !== "running" && (
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

        {items.isLoading ? (
          <Loading />
        ) : items.error ? (
          <ErrorBox message={(items.error as Error).message} />
        ) : !data?.items.length ? (
          <EmptyState icon={<ShieldCheck className="h-5 w-5" />} title="清单为空" desc="规则没有命中任何文件，可能已清理过或条件过严。" className="py-10" />
        ) : (
          <div className="max-h-[460px] overflow-auto rounded-md border border-border">
            <table className="table">
              <thead className="sticky top-0 z-10">
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
                {data.items.map((it) => (
                  <ItemRow
                    key={it.id}
                    item={it}
                    editable={editable}
                    onToggle={(v) => select.mutate({ id: it.id, selected: v })}
                  />
                ))}
              </tbody>
            </table>
          </div>
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
