import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ChevronDown,
  Copy,
  Eraser,
  Eye,
  Film,
  Images,
  Layers,
  Pencil,
  Plus,
  Power,
  ShieldAlert,
  Sparkles,
  Trash2,
} from "lucide-react";
import { api } from "../services/api";
import type { DryRunResult, GroupKind, Rule, RuleScope } from "../services/types";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Checkbox, Field, Input, Select, Switch, TagInput, Textarea } from "../components/ui/field";
import { ConfirmDialog, Dialog } from "../components/ui/dialog";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui/empty";
import { cn, formatBytes, formatNumber, groupKindLabel, kindLabel } from "../lib/utils";

const emptyScope = (): RuleScope => ({
  root_ids: [],
  volumes: [],
  dirs: [],
  exclude_dirs: [],
  kinds: [],
  ext_include: [],
  ext_exclude: [],
  min_size: 0,
  max_size: 0,
  name_include: [],
  name_exclude: [],
  name_regex: "",
});

function blankRule(): Rule {
  return {
    id: 0,
    name: "",
    description: "",
    enabled: true,
    priority: 100,
    auto_execute: false,
    require_confirmation: true,
    scope: emptyScope(),
    match: {
      group_kinds: ["exact"],
      min_similarity: 0,
      min_group_members: 2,
      require_exact_content: true,
      stale_days: 0,
      operator: "and",
      same_name: null,
    },
    retention: {
      strategy: "keep_best",
      keys: [
        { field: "resolution", order: "desc", weight: 4, value: "", prefer: [] },
        { field: "size", order: "desc", weight: 2, value: "", prefer: [] },
        { field: "depth", order: "asc", weight: 1, value: "", prefer: [] },
        { field: "mtime", order: "asc", weight: 1, value: "", prefer: [] },
      ],
      keep_count: 1,
      pinned_paths: [],
    },
    delete: {
      kinds: [],
      max_resolution: 0,
      min_resolution: 0,
      older_than_days: 0,
      path_regex: [],
      max_deletes_per_run: 0,
      min_reclaim_bytes: 0,
      keep_at_least_one: true,
    },
    associated: {
      enabled: false,
      trigger_extensions: [".mkv", ".mp4", ".avi"],
      associated_extensions: [".nfo", ".srt", ".ass"],
      same_directory_only: true,
      same_basename_only: true,
      require_confirmation: true,
      protect_referenced: true,
    },
    created_at: 0,
    updated_at: 0,
  };
}

export default function RulesPage() {
  const qc = useQueryClient();
  const rules = useQuery({ queryKey: ["rules"], queryFn: api.rules });
  const [editing, setEditing] = useState<Rule | null>(null);
  const [dryRunOpen, setDryRunOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Rule | null>(null);

  const toggle = useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) => api.toggleRule(id, enabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["rules"] }),
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.deleteRule(id),
    onSuccess: () => {
      setDeleteTarget(null);
      qc.invalidateQueries({ queryKey: ["rules"] });
    },
  });

  const save = useMutation({
    mutationFn: (r: Rule) => {
      const { id, created_at, updated_at, ...body } = r;
      void created_at;
      void updated_at;
      return id ? api.updateRule(id, r) : api.createRule(body);
    },
    onSuccess: () => {
      setEditing(null);
      qc.invalidateQueries({ queryKey: ["rules"] });
    },
  });

  const list = rules.data?.items ?? [];
  const enabledCount = list.filter((r) => r.enabled).length;

  return (
    <div className="pb-10">
      <PageHeader
        title="清理规则"
        desc="规则只负责「算出该删什么」，不会自动动文件。执行前必须先 Dry Run 确认。"
        actions={
          <>
            <Button
              variant="outline"
              size="sm"
              icon={<Eye className="h-3.5 w-3.5" />}
              onClick={() => setDryRunOpen(true)}
            >
              Dry Run 预览
            </Button>
            <Button
              variant="primary"
              size="sm"
              icon={<Plus className="h-3.5 w-3.5" />}
              onClick={() => setEditing(blankRule())}
            >
              新建规则
            </Button>
          </>
        }
      />

      <div className="space-y-3 px-4 sm:px-5">
        <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">
          <Badge tone="info">{list.length} 条规则</Badge>
          <Badge tone="success">{enabledCount} 条启用</Badge>
          <span>·</span>
          <span>默认全部需要人工确认，自动执行默认关闭</span>
        </div>

        {rules.isLoading ? (
          <Loading />
        ) : rules.error ? (
          <ErrorBox message={(rules.error as Error).message} onRetry={() => rules.refetch()} />
        ) : list.length === 0 ? (
          <Card>
            <EmptyState
              icon={<Sparkles className="h-5 w-5" />}
              title="还没有清理规则"
              desc="规则决定「同组里保留哪个、删除哪些」。可以基于重复组、分辨率、修改时间等条件组合。"
              action={
                <Button variant="primary" size="sm" onClick={() => setEditing(blankRule())}>
                  新建第一条规则
                </Button>
              }
            />
          </Card>
        ) : (
          list.map((r) => (
            <RuleCard
              key={r.id}
              rule={r}
              onEdit={() => setEditing(r)}
              onDelete={() => setDeleteTarget(r)}
              onToggle={(v) => toggle.mutate({ id: r.id, enabled: v })}
              toggling={toggle.isPending}
            />
          ))
        )}
      </div>

      {editing && (
        <RuleEditor
          rule={editing}
          onClose={() => setEditing(null)}
          onSave={(r) => save.mutate(r)}
          saving={save.isPending}
          error={save.isError ? (save.error as Error).message : undefined}
        />
      )}

      {dryRunOpen && <DryRunPanel onClose={() => setDryRunOpen(false)} />}

      <ConfirmDialog
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && remove.mutate(deleteTarget.id)}
        loading={remove.isPending}
        danger
        title="删除这条规则？"
        confirmText="删除"
        desc={`「${deleteTarget?.name}」将被永久移除。已生成的清理任务不受影响。`}
      />
    </div>
  );
}

function RuleCard({
  rule,
  onEdit,
  onDelete,
  onToggle,
  toggling,
}: {
  rule: Rule;
  onEdit: () => void;
  onDelete: () => void;
  onToggle: (v: boolean) => void;
  toggling: boolean;
}) {
  const [open, setOpen] = useState(false);
  const kinds = rule.match.group_kinds ?? [];

  return (
    <Card className={cn("overflow-hidden", !rule.enabled && "opacity-70")}>
      <div className="flex items-start gap-3 px-4 py-3">
        <div className={cn("mt-0.5 rounded-md p-1.5", rule.enabled ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground")}>
          <Layers className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-[13.5px] font-semibold">{rule.name}</span>
            <Badge tone="outline">优先级 {rule.priority}</Badge>
            {rule.auto_execute ? <Badge tone="danger">自动执行</Badge> : <Badge tone="success">需确认</Badge>}
            {rule.retention.strategy && <Badge tone="outline">{strategyLabel(rule.retention.strategy)}</Badge>}
          </div>
          {rule.description ? (
            <p className="mt-1 text-[12px] leading-relaxed text-muted-foreground">{rule.description}</p>
          ) : null}
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
            {kinds.map((k) => (
              <span key={k} className="rounded bg-muted px-1.5 py-0.5 text-[10.5px] text-muted-foreground">
                {groupKindLabel(k)}
              </span>
            ))}
            {rule.scope.kinds?.map((k) => (
              <span key={k} className="rounded bg-muted px-1.5 py-0.5 text-[10.5px] text-muted-foreground">
                仅 {kindLabel(k)}
              </span>
            ))}
            {rule.delete.max_resolution ? (
              <span className="rounded bg-warning/10 px-1.5 py-0.5 text-[10.5px] text-warning">
                仅删 ≤ {rule.delete.max_resolution}px
              </span>
            ) : null}
            {rule.delete.older_than_days ? (
              <span className="rounded bg-muted px-1.5 py-0.5 text-[10.5px] text-muted-foreground">
                &gt; {rule.delete.older_than_days} 天
              </span>
            ) : null}
            {rule.associated.enabled ? (
              <span className="rounded bg-muted px-1.5 py-0.5 text-[10.5px] text-muted-foreground">
                连带清理附属文件
              </span>
            ) : null}
          </div>
        </div>

        <div className="flex shrink-0 items-center gap-1">
          <button
            onClick={() => onToggle(!rule.enabled)}
            disabled={toggling}
            title={rule.enabled ? "停用" : "启用"}
            className={cn(
              "flex h-7 w-7 items-center justify-center rounded-md",
              rule.enabled ? "text-success hover:bg-success/10" : "text-muted-foreground hover:bg-muted",
            )}
          >
            <Power className="h-3.5 w-3.5" />
          </button>
          <button onClick={() => setOpen((v) => !v)} className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted">
            <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", open && "rotate-180")} />
          </button>
          <button onClick={onEdit} className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted">
            <Pencil className="h-3.5 w-3.5" />
          </button>
          <button onClick={onDelete} className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-destructive/10 hover:text-destructive">
            <Trash2 className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>

      {open && (
        <div className="grid gap-4 border-t border-border bg-muted/25 px-4 py-3 md:grid-cols-2 xl:grid-cols-4">
          <DetailBlock title="作用范围">
            <Row k="类型" v={rule.scope.kinds?.length ? rule.scope.kinds.map(kindLabel).join("、") : "全部"} />
            <Row k="目录前缀" v={rule.scope.dirs?.join("、") || "不限"} />
            <Row k="排除目录" v={rule.scope.exclude_dirs?.join("、") || "无"} />
            <Row k="扩展名" v={rule.scope.ext_include?.join(" ") || "不限"} />
            <Row
              k="大小"
              v={
                rule.scope.min_size || rule.scope.max_size
                  ? `${rule.scope.min_size ? formatBytes(rule.scope.min_size) : "0"} ~ ${
                      rule.scope.max_size ? formatBytes(rule.scope.max_size) : "∞"
                    }`
                  : "不限"
              }
            />
          </DetailBlock>
          <DetailBlock title="匹配条件">
            <Row k="重复类型" v={kinds.map(groupKindLabel).join("、") || "完全重复"} />
            <Row k="最少成员" v={`${rule.match.min_group_members} 个`} />
            {rule.match.min_similarity > 0 && <Row k="相似度下限" v={`${(rule.match.min_similarity * 100).toFixed(0)}%`} />}
            {rule.match.stale_days > 0 && <Row k="仅处理旧文件" v={`> ${rule.match.stale_days} 天`} />}
            <Row k="条件逻辑" v={rule.match.operator === "or" ? "任一满足" : "全部满足"} />
          </DetailBlock>
          <DetailBlock title="保留策略">
            <Row k="策略" v={strategyLabel(rule.retention.strategy)} />
            <Row k="每组保留" v={`${rule.retention.keep_count} 个`} />
            {rule.retention.keys?.slice(0, 4).map((k, i) => (
              <Row key={i} k={fieldLabel(k.field)} v={`${k.order === "asc" ? "越小" : "越大"}越优先 · 权重 ${k.weight}`} />
            ))}
            {rule.retention.pinned_paths?.length ? <Row k="钉住路径" v={`${rule.retention.pinned_paths.length} 条`} /> : null}
          </DetailBlock>
          <DetailBlock title="删除限制">
            <Row k="单次上限" v={rule.delete.max_deletes_per_run ? `${formatNumber(rule.delete.max_deletes_per_run)} 个` : "不限"} />
            <Row
              k="最小释放"
              v={rule.delete.min_reclaim_bytes ? formatBytes(rule.delete.min_reclaim_bytes) : "不限"}
            />
            {rule.delete.path_regex?.length ? <Row k="路径正则" v={`${rule.delete.path_regex.length} 条`} /> : null}
            <Row k="每组至少留一" v={rule.delete.keep_at_least_one ? "是" : "否"} />
          </DetailBlock>
        </div>
      )}
    </Card>
  );
}

function DetailBlock({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">{title}</div>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-baseline justify-between gap-2 text-[11.5px]">
      <span className="shrink-0 text-muted-foreground">{k}</span>
      <span className="truncate text-right" title={v}>
        {v}
      </span>
    </div>
  );
}

function strategyLabel(s: string): string {
  return {
    keep_best: "保留最优",
    keep_largest: "保留最大",
    keep_newest: "保留最新",
    keep_oldest: "保留最旧",
  }[s] ?? s;
}

function fieldLabel(f: string): string {
  return {
    resolution: "分辨率",
    bitrate: "码率",
    size: "体积",
    mtime: "修改时间",
    ctime: "创建时间",
    depth: "目录深度",
    duration: "时长",
    ext: "扩展名偏好",
    dir_prefix: "路径前缀",
    volume: "存储卷",
    name_regex: "文件名正则",
  }[f] ?? f;
}

/* ---------------- 编辑器 ---------------- */

type TabKey = "scope" | "match" | "retention" | "delete";

function RuleEditor({
  rule,
  onClose,
  onSave,
  saving,
  error,
}: {
  rule: Rule;
  onClose: () => void;
  onSave: (r: Rule) => void;
  saving: boolean;
  error?: string;
}) {
  const [draft, setDraft] = useState<Rule>(rule);
  const [tab, setTab] = useState<TabKey>("match");

  const patch = <K extends keyof Rule>(key: K, value: Rule[K]) => setDraft((d) => ({ ...d, [key]: value }));
  const patchMatch = (p: Partial<Rule["match"]>) => setDraft((d) => ({ ...d, match: { ...d.match, ...p } }));
  const patchScope = (p: Partial<Rule["scope"]>) => setDraft((d) => ({ ...d, scope: { ...d.scope, ...p } }));
  const patchRet = (p: Partial<Rule["retention"]>) => setDraft((d) => ({ ...d, retention: { ...d.retention, ...p } }));
  const patchDel = (p: Partial<Rule["delete"]>) => setDraft((d) => ({ ...d, delete: { ...d.delete, ...p } }));
  const patchAssoc = (p: Partial<Rule["associated"]>) => setDraft((d) => ({ ...d, associated: { ...d.associated, ...p } }));

  const kinds = draft.match.group_kinds ?? [];
  const toggleKind = (k: GroupKind) =>
    patchMatch({ group_kinds: kinds.includes(k) ? kinds.filter((x) => x !== k) : [...kinds, k] });

  return (
    <Dialog
      open
      onClose={onClose}
      width="max-w-3xl"
      title={draft.id ? "编辑规则" : "新建规则"}
      desc="规则是声明式的：作用范围 → 匹配 → 保留 → 删除限制，逐层收敛"
      footer={
        <>
          {error && <span className="mr-auto max-w-[360px] text-[12px] text-destructive">{error}</span>}
          <Button variant="ghost" size="sm" onClick={onClose}>
            取消
          </Button>
          <Button variant="primary" size="sm" loading={saving} onClick={() => onSave(draft)}>
            保存
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="规则名称">
            <Input value={draft.name} onChange={(e) => patch("name", e.target.value)} placeholder="例：清理重复的剧集备份" />
          </Field>
          <div className="flex items-end gap-4 pb-1">
            <Switch checked={draft.enabled} onChange={(v) => patch("enabled", v)} label="启用" />
            <Switch
              checked={draft.auto_execute}
              onChange={(v) => patch("auto_execute", v)}
              label="自动执行"
              hint="跳过确认（谨慎）"
            />
          </div>
        </div>
        <Field label="说明">
          <Textarea
            rows={2}
            value={draft.description}
            onChange={(e) => patch("description", e.target.value)}
            placeholder="这条规则用来解决什么问题"
          />
        </Field>

        {/* Tab 头 */}
        <div className="flex gap-1 rounded-lg bg-muted p-1">
          {(
            [
              ["match", "匹配"],
              ["scope", "范围"],
              ["retention", "保留"],
              ["delete", "删除限制"],
            ] as [TabKey, string][]
          ).map(([k, label]) => (
            <button
              key={k}
              onClick={() => setTab(k)}
              className={cn(
                "flex-1 rounded-md px-2 py-1.5 text-[12.5px] font-medium transition-colors",
                tab === k ? "bg-card shadow-sm" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {label}
            </button>
          ))}
        </div>

        {tab === "match" && (
          <div className="space-y-3">
            <Field label="重复类型" hint="可多选，满足任一类即进入规则">
              <div className="flex flex-wrap gap-1.5">
                {[
                  { k: "exact", l: "完全重复", icon: <Copy className="h-3 w-3" /> },
                  { k: "image", l: "相似图片", icon: <Images className="h-3 w-3" /> },
                  { k: "video", l: "相似视频", icon: <Film className="h-3 w-3" /> },
                ].map((o) => (
                  <button
                    key={o.k}
                    onClick={() => toggleKind(o.k as GroupKind)}
                    className={cn(
                      "flex items-center gap-1 rounded-md border px-2 py-1 text-[12px]",
                      kinds.includes(o.k as GroupKind)
                        ? "border-primary bg-primary/10 text-primary"
                        : "border-border text-muted-foreground hover:bg-muted",
                    )}
                  >
                    {o.icon}
                    {o.l}
                  </button>
                ))}
              </div>
            </Field>

            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="组内最少文件数">
                <Input
                  type="number"
                  min={2}
                  value={draft.match.min_group_members}
                  onChange={(e) => patchMatch({ min_group_members: Number(e.target.value) || 2 })}
                />
              </Field>
              <Field label="相似度下限" hint="0-1，0 表示用默认">
                <Input
                  type="number"
                  step="0.01"
                  min={0}
                  max={1}
                  value={draft.match.min_similarity}
                  onChange={(e) => patchMatch({ min_similarity: Number(e.target.value) || 0 })}
                />
              </Field>
              <Field label="仅处理旧于 N 天" hint="0 表示不限">
                <Input
                  type="number"
                  min={0}
                  value={draft.match.stale_days}
                  onChange={(e) => patchMatch({ stale_days: Number(e.target.value) || 0 })}
                />
              </Field>
            </div>

            <div className="flex flex-wrap gap-6">
              <Switch
                checked={draft.match.require_exact_content}
                onChange={(v) => patchMatch({ require_exact_content: v })}
                label="要求全文校验通过"
                hint="仅完全重复有意义"
              />
              <Field label="条件逻辑" className="min-w-[160px]">
                <Select
                  value={draft.match.operator}
                  onChange={(e) => patchMatch({ operator: e.target.value as "and" | "or" })}
                  className="h-8 text-[12px]"
                >
                  <option value="and">全部满足 (and)</option>
                  <option value="or">任一满足 (or)</option>
                </Select>
              </Field>
            </div>
          </div>
        )}

        {tab === "scope" && (
          <div className="space-y-3">
            <Field label="限定媒体类型" hint="留空 = 全部">
              <div className="flex flex-wrap gap-1.5">
                {["video", "image", "audio", "document"].map((k) => {
                  const cur = draft.scope.kinds ?? [];
                  const on = cur.includes(k);
                  return (
                    <button
                      key={k}
                      onClick={() => patchScope({ kinds: on ? cur.filter((x) => x !== k) : [...cur, k] })}
                      className={cn(
                        "rounded-md border px-2 py-1 text-[12px]",
                        on ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:bg-muted",
                      )}
                    >
                      {kindLabel(k)}
                    </button>
                  );
                })}
              </div>
            </Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="只包含扩展名" hint="如 .mp4 .mkv">
                <TagInput value={draft.scope.ext_include ?? []} onChange={(v) => patchScope({ ext_include: v })} mono />
              </Field>
              <Field label="排除扩展名">
                <TagInput value={draft.scope.ext_exclude ?? []} onChange={(v) => patchScope({ ext_exclude: v })} mono />
              </Field>
              <Field label="限定目录前缀" hint="任一路径包含即可">
                <TagInput value={draft.scope.dirs ?? []} onChange={(v) => patchScope({ dirs: v })} mono />
              </Field>
              <Field label="排除目录前缀">
                <TagInput value={draft.scope.exclude_dirs ?? []} onChange={(v) => patchScope({ exclude_dirs: v })} mono />
              </Field>
              <Field label="最小体积 (MB)" hint="0 = 不限">
                <Input
                  type="number"
                  min={0}
                  value={draft.scope.min_size ? draft.scope.min_size / 1048576 : ""}
                  onChange={(e) => patchScope({ min_size: Math.round((Number(e.target.value) || 0) * 1048576) })}
                  placeholder="0"
                />
              </Field>
              <Field label="最大体积 (MB)" hint="0 = 不限">
                <Input
                  type="number"
                  min={0}
                  value={draft.scope.max_size ? draft.scope.max_size / 1048576 : ""}
                  onChange={(e) => patchScope({ max_size: Math.round((Number(e.target.value) || 0) * 1048576) })}
                  placeholder="0"
                />
              </Field>
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="文件名包含" hint="子串匹配，忽略大小写">
                <TagInput value={draft.scope.name_include ?? []} onChange={(v) => patchScope({ name_include: v })} />
              </Field>
              <Field label="文件名排除">
                <TagInput value={draft.scope.name_exclude ?? []} onChange={(v) => patchScope({ name_exclude: v })} />
              </Field>
            </div>
            <Field label="文件名正则" hint="留空不使用">
              <Input
                value={draft.scope.name_regex ?? ""}
                onChange={(e) => patchScope({ name_regex: e.target.value })}
                placeholder="例：S\\d{2}E\\d{2}"
                className="font-mono text-[12px]"
              />
            </Field>
          </div>
        )}

        {tab === "retention" && (
          <div className="space-y-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="保留策略">
                <Select
                  value={draft.retention.strategy}
                  onChange={(e) => patchRet({ strategy: e.target.value })}
                >
                  <option value="keep_best">保留最优（按权重综合评分）</option>
                  <option value="keep_largest">保留体积最大</option>
                  <option value="keep_newest">保留最新</option>
                  <option value="keep_oldest">保留最旧</option>
                </Select>
              </Field>
              <Field label="每组至少保留">
                <Input
                  type="number"
                  min={1}
                  value={draft.retention.keep_count}
                  onChange={(e) => patchRet({ keep_count: Number(e.target.value) || 1 })}
                />
              </Field>
            </div>

            <div>
              <div className="label">评分维度（顺序即优先级，权重越大越重要）</div>
              <div className="space-y-1.5">
                {(draft.retention.keys ?? []).map((k, i) => (
                  <div key={i} className="flex items-center gap-2 rounded-md border border-border px-2 py-1.5">
                    <Select
                      value={k.field}
                      onChange={(e) => {
                        const next = [...draft.retention.keys];
                        next[i] = { ...k, field: e.target.value };
                        patchRet({ keys: next });
                      }}
                      className="h-7 w-[130px] text-[12px]"
                    >
                      {["resolution", "bitrate", "size", "duration", "mtime", "ctime", "depth"].map((f) => (
                        <option key={f} value={f}>
                          {fieldLabel(f)}
                        </option>
                      ))}
                    </Select>
                    <Select
                      value={k.order}
                      onChange={(e) => {
                        const next = [...draft.retention.keys];
                        next[i] = { ...k, order: e.target.value as "asc" | "desc" };
                        patchRet({ keys: next });
                      }}
                      className="h-7 w-[92px] text-[12px]"
                    >
                      <option value="desc">越大越好</option>
                      <option value="asc">越小越好</option>
                    </Select>
                    <Input
                      type="number"
                      step="0.5"
                      min={0}
                      value={k.weight}
                      onChange={(e) => {
                        const next = [...draft.retention.keys];
                        next[i] = { ...k, weight: Number(e.target.value) || 1 };
                        patchRet({ keys: next });
                      }}
                      className="h-7 w-[74px] text-[12px]"
                    />
                    <span className="text-[11px] text-muted-foreground">权重</span>
                    <button
                      onClick={() => patchRet({ keys: draft.retention.keys.filter((_, x) => x !== i) })}
                      className="ml-auto rounded p-1 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                    >
                      <Trash2 className="h-3 w-3" />
                    </button>
                  </div>
                ))}
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<Plus className="h-3 w-3" />}
                  onClick={() =>
                    patchRet({
                      keys: [...(draft.retention.keys ?? []), { field: "size", order: "desc", weight: 1, value: "", prefer: [] }],
                    })
                  }
                >
                  添加维度
                </Button>
              </div>
            </div>

            <Field label="必须保留的路径" hint="命中即永不删除">
              <TagInput value={draft.retention.pinned_paths ?? []} onChange={(v) => patchRet({ pinned_paths: v })} mono />
            </Field>
          </div>
        )}

        {tab === "delete" && (
          <div className="space-y-3">
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="单次最多删除" hint="0 = 不限，防误伤">
                <Input
                  type="number"
                  min={0}
                  value={draft.delete.max_deletes_per_run || ""}
                  onChange={(e) => patchDel({ max_deletes_per_run: Number(e.target.value) || 0 })}
                  placeholder="不限"
                />
              </Field>
              <Field label="低于该分辨率才删" hint="像素面积，如 921600=720p">
                <Input
                  type="number"
                  min={0}
                  value={draft.delete.max_resolution || ""}
                  onChange={(e) => patchDel({ max_resolution: Number(e.target.value) || 0 })}
                  placeholder="不限"
                />
              </Field>
              <Field label="仅删旧于 N 天" hint="0 = 不限">
                <Input
                  type="number"
                  min={0}
                  value={draft.delete.older_than_days || ""}
                  onChange={(e) => patchDel({ older_than_days: Number(e.target.value) || 0 })}
                  placeholder="不限"
                />
              </Field>
            </div>

            <Field label="路径正则白名单" hint="命中任一才允许删除">
              <TagInput value={draft.delete.path_regex ?? []} onChange={(v) => patchDel({ path_regex: v })} mono />
            </Field>

            <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 space-y-2.5">
              <Switch
                checked={draft.associated.enabled}
                onChange={(v) => patchAssoc({ enabled: v })}
                label="连带清理附属文件"
                hint="删除主文件时，同步把同目录同名的 NFO/字幕/封面一起隔离"
              />
              {draft.associated.enabled && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <Field label="主文件扩展名">
                    <TagInput
                      value={draft.associated.trigger_extensions ?? []}
                      onChange={(v) => patchAssoc({ trigger_extensions: v })}
                      mono
                    />
                  </Field>
                  <Field label="连带清理的扩展名">
                    <TagInput
                      value={draft.associated.associated_extensions ?? []}
                      onChange={(v) => patchAssoc({ associated_extensions: v })}
                      mono
                    />
                  </Field>
                </div>
              )}
            </div>

            <div className="flex items-start gap-2 rounded-md border border-warning/30 bg-warning/8 px-3 py-2 text-[11.5px] text-warning">
              <ShieldAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              保存后建议先 Dry Run，确认命中数量和释放空间符合预期，再创建清理任务。
            </div>
          </div>
        )}
      </div>
    </Dialog>
  );
}

/* ---------------- Dry Run ---------------- */

function DryRunPanel({ onClose }: { onClose: () => void }) {
  const rules = useQuery({ queryKey: ["rules"], queryFn: api.rules });
  const [picked, setPicked] = useState<number[]>([]);
  const [result, setResult] = useState<DryRunResult | null>(null);

  const run = useMutation({
    mutationFn: () => api.dryRun(picked, 2000),
    onSuccess: (r) => setResult(r),
  });

  const enabledRules = useMemo(() => (rules.data?.items ?? []).filter((r) => r.enabled), [rules.data]);

  return (
    <Dialog
      open
      onClose={onClose}
      width="max-w-3xl"
      title="Dry Run 预览"
      desc="只计算不执行：看看规则会命中哪些文件、释放多少空间"
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={onClose}>
            关闭
          </Button>
          <Button
            variant="primary"
            size="sm"
            icon={<Eye className="h-3.5 w-3.5" />}
            loading={run.isPending}
            onClick={() => run.mutate()}
          >
            开始计算
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <div>
          <div className="label">选择规则（不选 = 全部启用规则）</div>
          <div className="space-y-1">
            {enabledRules.map((r) => (
              <label key={r.id} className="flex cursor-pointer items-center gap-2 rounded-md px-1.5 py-1 hover:bg-muted/60">
                <Checkbox
                  checked={picked.includes(r.id)}
                  onChange={(v) => setPicked((p) => (v ? [...p, r.id] : p.filter((x) => x !== r.id)))}
                  label={
                    <span className="flex items-center gap-1.5">
                      {r.name}
                      <span className="text-[11px] text-muted-foreground">
                        ({r.match.group_kinds.map(groupKindLabel).join("+")})
                      </span>
                    </span>
                  }
                />
              </label>
            ))}
            {enabledRules.length === 0 && (
              <div className="text-[12px] text-muted-foreground">没有启用的规则。</div>
            )}
          </div>
        </div>

        {run.isError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/8 px-3 py-2 text-[12px] text-destructive">
            {(run.error as Error).message}
          </div>
        )}

        {result && (
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <Metric label="命中文件" value={formatNumber(result.total_items)} />
              <Metric label="涉及分组" value={formatNumber(result.group_count)} />
              <Metric label="可释放" value={formatBytes(result.total_bytes)} />
              <Metric label="是否截断" value={result.truncated ? "是" : "否"} />
            </div>

            {result.notes?.length ? (
              <div className="rounded-md border border-border bg-muted/40 px-3 py-2 text-[11.5px] text-muted-foreground">
                {result.notes.map((n, i) => (
                  <div key={i}>· {n}</div>
                ))}
              </div>
            ) : null}

            <div className="max-h-[300px] overflow-auto rounded-md border border-border">
              <table className="table">
                <thead className="sticky top-0">
                  <tr>
                    <th>文件</th>
                    <th className="w-[72px]">类型</th>
                    <th className="w-[88px]">大小</th>
                    <th>原因</th>
                  </tr>
                </thead>
                <tbody>
                  {result.items.slice(0, 200).map((it) => (
                    <tr key={it.file_id}>
                      <td className="max-w-[280px]">
                        <div className="truncate text-[12px]" title={it.path}>
                          {it.name}
                        </div>
                        <div className="truncate text-[10.5px] text-muted-foreground" title={it.path}>
                          {it.path}
                        </div>
                      </td>
                      <td className="text-[11.5px]">{kindLabel(it.media_kind)}</td>
                      <td className="text-[11.5px] tabular-nums">{formatBytes(it.size)}</td>
                      <td className="max-w-[220px] truncate text-[11.5px] text-muted-foreground" title={it.reason}>
                        {it.reason}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {result.items.length > 200 && (
              <div className="text-center text-[11.5px] text-muted-foreground">
                仅显示前 200 条，共 {formatNumber(result.total_items)} 条
              </div>
            )}
          </div>
        )}
      </div>
    </Dialog>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border border-border px-3 py-2">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-[15px] font-semibold tabular-nums">{value}</div>
    </div>
  );
}
