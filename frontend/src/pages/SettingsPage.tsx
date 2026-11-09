import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  CheckCircle2,
  Cpu,
  FolderOpen,
  HardDrive,
  Info,
  KeyRound,
  Plus,
  Save,
  Trash2,
  UserPlus,
  Users,
} from "lucide-react";
import { api } from "../services/api";
import type { Root, SettingsPayload, User } from "../services/types";
import { Badge, Card, CardBody, CardHeader, CardTitle } from "../components/ui/card";
import { Button } from "../components/ui/button";
import { Checkbox, Field, Input, Select, Switch, TagInput } from "../components/ui/field";
import { ConfirmDialog, Dialog } from "../components/ui/dialog";
import { DirectoryPicker } from "../components/DirectoryPicker";
import { EmptyState, ErrorBox, Loading, PageHeader } from "../components/ui/empty";
import { useAuth } from "../lib/auth";
import { cn, formatBytes, formatDate, formatNumber } from "../lib/utils";

type Tab = "roots" | "config" | "users" | "about";

export default function SettingsPage() {
  const [tab, setTab] = useState<Tab>("roots");
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const tabs: [Tab, string][] = [
    ["roots", "存储卷"],
    ["config", "扫描与清理参数"],
    ["users", "账号"],
    ["about", "关于与诊断"],
  ];

  return (
    <div className="pb-10">
      <PageHeader title="系统设置" desc="存储卷、默认参数、账号与运行环境诊断。" />

      <div className="px-4 sm:px-5">
        <div className="mb-3 flex gap-1 overflow-x-auto rounded-lg bg-muted p-1">
          {tabs.map(([k, label]) => (
            <button
              key={k}
              onClick={() => setTab(k)}
              className={cn(
                "shrink-0 rounded-md px-3 py-1.5 text-[12.5px] font-medium transition-colors",
                tab === k ? "bg-card shadow-sm" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {label}
            </button>
          ))}
        </div>

        {tab === "roots" && <RootsPanel isAdmin={isAdmin} />}
        {tab === "config" && <ConfigPanel isAdmin={isAdmin} />}
        {tab === "users" && <UsersPanel isAdmin={isAdmin} currentUserId={user?.id} />}
        {tab === "about" && <AboutPanel />}
      </div>
    </div>
  );
}

/* ---------------- 存储卷 ---------------- */

function RootsPanel({ isAdmin }: { isAdmin: boolean }) {
  const qc = useQueryClient();
  const roots = useQuery({ queryKey: ["roots"], queryFn: api.roots });
  const info = useQuery({ queryKey: ["system-info"], queryFn: api.systemInfo });
  const [addOpen, setAddOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Root | null>(null);

  const update = useMutation({
    mutationFn: ({ id, body }: { id: number; body: Partial<{ name: string; enabled: boolean; writable: boolean }> }) =>
      api.updateRoot(id, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["roots"] }),
  });

  const remove = useMutation({
    mutationFn: (id: number) => api.deleteRoot(id),
    onSuccess: () => {
      setDeleteTarget(null);
      qc.invalidateQueries({ queryKey: ["roots"] });
    },
  });

  const list = roots.data?.items ?? [];

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader
          action={
            isAdmin ? (
              <Button size="sm" variant="primary" icon={<Plus className="h-3.5 w-3.5" />} onClick={() => setAddOpen(true)}>
                添加存储卷
              </Button>
            ) : (
              <Badge tone="outline">只读账号不可修改</Badge>
            )
          }
        >
          <CardTitle hint={info.data ? `数据根目录：${info.data.data_root}` : undefined}>
            已注册的存储卷
          </CardTitle>
        </CardHeader>

        {roots.isLoading ? (
          <Loading />
        ) : roots.error ? (
          <ErrorBox message={(roots.error as Error).message} />
        ) : list.length === 0 ? (
          <EmptyState
            icon={<HardDrive className="h-5 w-5" />}
            title="还没有存储卷"
            desc="存储卷是扫描的入口。群晖上通常是 /volume1/photo、/volume1/video 之类的挂载点。"
            action={
              isAdmin ? (
                <Button size="sm" variant="primary" onClick={() => setAddOpen(true)}>
                  添加第一个存储卷
                </Button>
              ) : undefined
            }
          />
        ) : (
          <div className="divide-y divide-border/70">
            {list.map((r) => (
              <div key={r.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <div className={cn("rounded-md p-2", r.enabled ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground")}>
                  <FolderOpen className="h-4 w-4" />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span className="text-[13px] font-medium">{r.name}</span>
                    {r.volume ? <Badge tone="outline">{r.volume}</Badge> : null}
                    {r.writable ? <Badge tone="warn">可写</Badge> : <Badge tone="success">只读</Badge>}
                    {!r.enabled && <Badge tone="outline">已停用</Badge>}
                  </div>
                  <div className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground" title={r.real_path}>
                    {r.real_path}
                  </div>
                  <div className="mt-0.5 text-[10.5px] text-muted-foreground">
                    {formatNumber(r.file_count)} 个文件 · {formatBytes(r.total_size)}
                    {r.host_path && r.host_path !== r.real_path ? ` · 容器内 ${r.host_path}` : ""}
                  </div>
                </div>
                {isAdmin && (
                  <div className="flex items-center gap-3">
                    <Switch checked={r.enabled} onChange={(v) => update.mutate({ id: r.id, body: { enabled: v } })} label="启用" />
                    <Switch checked={r.writable} onChange={(v) => update.mutate({ id: r.id, body: { writable: v } })} label="允许清理" />
                    <button
                      onClick={() => setDeleteTarget(r)}
                      className="rounded p-1.5 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </Card>

      {isAdmin && (
        <Card className="border-warning/30 bg-warning/[0.04]">
          <CardBody className="flex items-start gap-2.5 text-[12px] text-warning">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <div>
              「允许清理」默认关闭。只有开启后，该卷下的文件才会被清理任务移入隔离区；
              关闭状态下所有匹配文件只会出现在清单里，不会被执行。
            </div>
          </CardBody>
        </Card>
      )}

      {addOpen && <AddRootDialog onClose={() => setAddOpen(false)} />}

      <ConfirmDialog
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && remove.mutate(deleteTarget.id)}
        loading={remove.isPending}
        danger
        title="移除这个存储卷？"
        confirmText="移除"
        desc={
          <>
            「{deleteTarget?.name}」将从扫描范围中移除，索引数据会保留但不再更新。
            <b className="text-foreground">磁盘上的文件不会被删除。</b>
          </>
        }
      />
    </div>
  );
}

function AddRootDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [writable, setWritable] = useState(false);
  const [browsing, setBrowsing] = useState(false);

  const add = useMutation({
    mutationFn: () => api.addRoot(name.trim() || path.split(/[\\/]/).filter(Boolean).pop() || "存储卷", path.trim(), writable),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["roots"] });
      onClose();
    },
  });

  return (
    <>
      <Dialog
        open
        onClose={onClose}
        title="添加存储卷"
        desc="填写容器内可访问的绝对路径"
        footer={
          <>
            {add.isError && <span className="mr-auto text-[12px] text-destructive">{(add.error as Error).message}</span>}
            <Button size="sm" variant="ghost" onClick={onClose}>
              取消
            </Button>
            <Button size="sm" variant="primary" loading={add.isPending} disabled={!path.trim()} onClick={() => add.mutate()}>
              添加
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field label="路径" hint="例如 /volume1/photo">
            <div className="flex gap-2">
              <Input value={path} onChange={(e) => setPath(e.target.value)} placeholder="/volume1/photo" className="font-mono text-[12px]" />
              <Button size="sm" variant="outline" onClick={() => setBrowsing(true)}>
                浏览
              </Button>
            </div>
          </Field>
          <Field label="显示名称" hint="留空则取路径最后一段">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="照片库" />
          </Field>
          <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5">
            <Switch
              checked={writable}
              onChange={setWritable}
              label="允许清理此卷"
              hint="开启后该卷的文件才能被移入隔离区。建议先保持关闭，确认扫描结果无误后再开启。"
            />
          </div>
        </div>
      </Dialog>

      {browsing && (
        <DirectoryPicker
          open={browsing}
          onClose={() => setBrowsing(false)}
          title="选择存储卷目录"
          desc="只能浏览已挂载进容器的媒体目录与已注册存储卷"
          pickLabel="使用此目录"
          onPick={(p) => {
            setPath(p);
            setBrowsing(false);
          }}
        />
      )}
    </>
  );
}

/* ---------------- 参数 ---------------- */

function ConfigPanel({ isAdmin }: { isAdmin: boolean }) {
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: ["settings"], queryFn: api.settings });
  const [draft, setDraft] = useState<Record<string, unknown>>({});
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (!settings.data) return;
    const merged: Record<string, unknown> = { ...settings.data.defaults, ...settings.data.values };
    setDraft(merged);
  }, [settings.data]);

  const save = useMutation({
    mutationFn: () => api.saveSettings(draft),
    onSuccess: () => {
      setSaved(true);
      setTimeout(() => setSaved(false), 2500);
      qc.invalidateQueries({ queryKey: ["settings"] });
    },
  });

  if (settings.isLoading) return <Loading />;
  if (settings.error) return <ErrorBox message={(settings.error as Error).message} />;
  const s: SettingsPayload = settings.data!;
  const keys = Object.keys(s.schema);

  const set = (k: string, v: unknown) => setDraft((d) => ({ ...d, [k]: v }));

  const groups: { title: string; keys: string[] }[] = [
    { title: "扫描默认值", keys: ["default_profile", "default_threads", "default_limit_mbps", "default_min_file_size", "default_exclude_names"] },
    { title: "隔离与安全", keys: ["quarantine_retention_days", "auto_purge_expired", "confirm_before_execute"] },
    { title: "定时任务", keys: ["scheduled_scan_enabled", "scheduled_scan_freq", "scheduled_scan_at"] },
    { title: "界面", keys: ["theme"] },
  ];

  return (
    <div className="space-y-4">
      {groups.map((g) => (
        <Card key={g.title}>
          <CardHeader>
            <CardTitle>{g.title}</CardTitle>
          </CardHeader>
          <CardBody className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {g.keys
              .filter((k) => keys.includes(k))
              .map((k) => (
                <SettingField
                  key={k}
                  name={k}
                  label={s.schema[k]}
                  value={draft[k]}
                  def={s.defaults[k]}
                  disabled={!isAdmin}
                  onChange={(v) => set(k, v)}
                />
              ))}
          </CardBody>
        </Card>
      ))}

      <div className="flex items-center justify-between rounded-lg border border-border bg-card px-4 py-3">
        <div className="text-[12px] text-muted-foreground">
          {isAdmin ? "修改后点击保存生效；部分参数在下一次任务开始时应用。" : "只读账号无法修改参数。"}
        </div>
        <div className="flex items-center gap-2">
          {save.isError && <span className="text-[12px] text-destructive">{(save.error as Error).message}</span>}
          {saved && (
            <span className="flex items-center gap-1 text-[12px] text-success">
              <CheckCircle2 className="h-3.5 w-3.5" /> 已保存
            </span>
          )}
          <Button
            size="sm"
            variant="primary"
            icon={<Save className="h-3.5 w-3.5" />}
            loading={save.isPending}
            disabled={!isAdmin}
            onClick={() => save.mutate()}
          >
            保存设置
          </Button>
        </div>
      </div>
    </div>
  );
}

function SettingField({
  name,
  label,
  value,
  def,
  disabled,
  onChange,
}: {
  name: string;
  label: string;
  value: unknown;
  def: unknown;
  disabled?: boolean;
  onChange: (v: unknown) => void;
}) {
  const isBool = typeof def === "boolean";
  const isNumber = typeof def === "number";
  const isArray = Array.isArray(def);
  const isSelect = name === "default_profile" || name === "scheduled_scan_freq" || name === "theme";

  if (isBool) {
    return (
      <div className="flex items-start pt-1">
        <Switch checked={Boolean(value)} onChange={onChange} disabled={disabled} label={label} />
      </div>
    );
  }

  return (
    <Field label={label}>
      {isSelect ? (
        <Select
          value={String(value ?? "")}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
        >
          {name === "default_profile" && (
            <>
              <option value="fast">极速：仅完全重复</option>
              <option value="standard">标准：含相似图片</option>
              <option value="full">深度：含相似视频</option>
            </>
          )}
          {name === "scheduled_scan_freq" && (
            <>
              <option value="daily">每天</option>
              <option value="weekly">每周</option>
              <option value="monthly">每月</option>
            </>
          )}
          {name === "theme" && (
            <>
              <option value="system">跟随系统</option>
              <option value="light">浅色</option>
              <option value="dark">深色</option>
            </>
          )}
        </Select>
      ) : isArray ? (
        <TagInput
          value={Array.isArray(value) ? (value as string[]) : []}
          onChange={(v) => onChange(v)}
          mono
        />
      ) : (
        <Input
          type={isNumber ? "number" : name === "scheduled_scan_at" ? "time" : "text"}
          value={value === undefined || value === null ? "" : String(value)}
          onChange={(e) => onChange(isNumber ? Number(e.target.value) || 0 : e.target.value)}
          disabled={disabled}
          className="text-[12.5px]"
        />
      )}
    </Field>
  );
}

/* ---------------- 账号 ---------------- */

function UsersPanel({ isAdmin, currentUserId }: { isAdmin: boolean; currentUserId?: number }) {
  const qc = useQueryClient();
  const users = useQuery({ queryKey: ["users"], queryFn: api.users, enabled: isAdmin });
  const [addOpen, setAddOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<User | null>(null);
  const [pwdOpen, setPwdOpen] = useState(false);

  const remove = useMutation({
    mutationFn: (id: number) => api.deleteUser(id),
    onSuccess: () => {
      setDeleteTarget(null);
      qc.invalidateQueries({ queryKey: ["users"] });
    },
  });

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader
          action={
            <div className="flex items-center gap-1.5">
              <Button size="sm" variant="outline" icon={<KeyRound className="h-3.5 w-3.5" />} onClick={() => setPwdOpen(true)}>
                修改我的密码
              </Button>
              {isAdmin && (
                <Button size="sm" variant="primary" icon={<UserPlus className="h-3.5 w-3.5" />} onClick={() => setAddOpen(true)}>
                  新建账号
                </Button>
              )}
            </div>
          }
        >
          <CardTitle hint="管理员可管理存储卷与参数，只读账号仅能浏览">账号列表</CardTitle>
        </CardHeader>

        {!isAdmin ? (
          <CardBody className="text-[12.5px] text-muted-foreground">只有管理员可以查看账号列表。</CardBody>
        ) : users.isLoading ? (
          <Loading />
        ) : (
          <div className="divide-y divide-border/70">
            {(users.data?.items ?? []).map((u) => (
              <div key={u.id} className="flex items-center gap-3 px-4 py-2.5">
                <div className="flex h-7 w-7 items-center justify-center rounded-full bg-primary/15 text-[11px] font-semibold text-primary">
                  {u.username.slice(0, 1).toUpperCase()}
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <span className="text-[13px] font-medium">{u.username}</span>
                    {u.role === "admin" ? <Badge tone="info">管理员</Badge> : <Badge tone="outline">只读</Badge>}
                    {u.id === currentUserId && <Badge tone="success">当前账号</Badge>}
                    {u.disabled && <Badge tone="danger">已禁用</Badge>}
                  </div>
                  <div className="text-[10.5px] text-muted-foreground">
                    创建于 {formatDate(u.created_at)}
                    {u.last_login_at ? ` · 最近登录 ${formatDate(u.last_login_at)}` : " · 从未登录"}
                  </div>
                </div>
                {u.id !== currentUserId && (
                  <button
                    onClick={() => setDeleteTarget(u)}
                    className="rounded p-1.5 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
      </Card>

      {addOpen && <AddUserDialog onClose={() => setAddOpen(false)} />}
      {pwdOpen && <ChangePasswordDialog onClose={() => setPwdOpen(false)} />}

      <ConfirmDialog
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && remove.mutate(deleteTarget.id)}
        loading={remove.isPending}
        danger
        title="删除这个账号？"
        confirmText="删除"
        desc={`账号「${deleteTarget?.username}」将无法再登录。`}
      />
    </div>
  );
}

function AddUserDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<"admin" | "viewer">("viewer");

  const create = useMutation({
    mutationFn: () => api.createUser(username.trim(), password, role),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["users"] });
      onClose();
    },
  });

  return (
    <Dialog
      open
      onClose={onClose}
      title="新建账号"
      width="max-w-md"
      footer={
        <>
          {create.isError && <span className="mr-auto text-[12px] text-destructive">{(create.error as Error).message}</span>}
          <Button size="sm" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button
            size="sm"
            variant="primary"
            loading={create.isPending}
            disabled={!username.trim() || password.length < 8}
            onClick={() => create.mutate()}
          >
            创建
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field label="用户名">
          <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
        </Field>
        <Field label="密码" hint="至少 8 位">
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
        </Field>
        <Field label="角色">
          <Select value={role} onChange={(e) => setRole(e.target.value as "admin" | "viewer")}>
            <option value="viewer">只读：可浏览与导出，不能修改</option>
            <option value="admin">管理员：完整权限</option>
          </Select>
        </Field>
      </div>
    </Dialog>
  );
}

function ChangePasswordDialog({ onClose }: { onClose: () => void }) {
  const [oldPwd, setOldPwd] = useState("");
  const [newPwd, setNewPwd] = useState("");
  const [confirm, setConfirm] = useState("");
  const [done, setDone] = useState(false);

  const change = useMutation({
    mutationFn: () => api.changePassword(oldPwd, newPwd),
    onSuccess: () => setDone(true),
  });

  const mismatch = confirm.length > 0 && confirm !== newPwd;

  return (
    <Dialog
      open
      onClose={onClose}
      title="修改密码"
      width="max-w-md"
      footer={
        <>
          <Button size="sm" variant="ghost" onClick={onClose}>
            关闭
          </Button>
          <Button
            size="sm"
            variant="primary"
            loading={change.isPending}
            disabled={done || !oldPwd || newPwd.length < 8 || newPwd !== confirm}
            onClick={() => change.mutate()}
          >
            保存
          </Button>
        </>
      }
    >
      {done ? (
        <div className="flex items-center gap-2 rounded-md border border-success/30 bg-success/8 px-3 py-2.5 text-[12.5px] text-success">
          <CheckCircle2 className="h-4 w-4" /> 密码已更新，其他设备的登录状态仍然有效。
        </div>
      ) : (
        <div className="space-y-3">
          <Field label="当前密码">
            <Input type="password" value={oldPwd} onChange={(e) => setOldPwd(e.target.value)} autoComplete="current-password" />
          </Field>
          <Field label="新密码" hint="至少 8 位">
            <Input type="password" value={newPwd} onChange={(e) => setNewPwd(e.target.value)} autoComplete="new-password" />
          </Field>
          <Field label="确认新密码">
            <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" />
          </Field>
          {mismatch && <div className="text-[12px] text-destructive">两次输入不一致</div>}
          {change.isError && <div className="text-[12px] text-destructive">{(change.error as Error).message}</div>}
        </div>
      )}
    </Dialog>
  );
}

/* ---------------- 关于 ---------------- */

function AboutPanel() {
  const info = useQuery({ queryKey: ["system-info"], queryFn: api.systemInfo });
  const me = useQuery({ queryKey: ["me"], queryFn: api.me });

  if (info.isLoading) return <Loading />;
  if (info.error) return <ErrorBox message={(info.error as Error).message} />;
  const d = info.data!;
  const caps = d.capabilities;

  const capList = useMemo(
    () => [
      { key: "ffmpeg", label: "ffmpeg", on: caps.ffmpeg, note: "视频抽帧、转码、缩略图" },
      { key: "ffprobe", label: "ffprobe", on: caps.ffprobe, note: "精确读取媒体元数据" },
      { key: "video_thumbnail", label: "视频缩略图", on: caps.video_thumbnail, note: "列表里显示视频首帧" },
      { key: "video_transcode", label: "在线转码预览", on: caps.video_transcode, note: "浏览器无法直放的格式" },
      { key: "video_similarity", label: "视频相似度", on: caps.video_similarity, note: "关键帧 pHash 比对" },
      { key: "native_probe", label: "内置解析器", on: caps.native_probe, note: "无 ffprobe 时的兜底" },
      { key: "image_similarity", label: "图片相似度", on: caps.image_similarity, note: "纯 Go pHash" },
    ],
    [caps],
  );

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader action={<Badge tone="info">v{d.version}</Badge>}>
          <CardTitle hint="NAS 媒体清理与去重">{d.app}</CardTitle>
        </CardHeader>
        <CardBody className="grid gap-x-6 gap-y-2 text-[12px] sm:grid-cols-2">
          <KV k="数据根目录" v={d.data_root} mono />
          <KV k="程序目录" v={d.home} mono />
          <KV k="索引文件" v={`${formatNumber(d.indexed_files)} · ${formatBytes(d.indexed_bytes)}`} />
          <KV k="重复分组" v={formatNumber(d.groups)} />
          <KV k="隔离区" v={`${formatNumber(d.quarantine.count)} 个 · ${formatBytes(d.quarantine.bytes)}`} />
          <KV k="默认线程" v={`${d.threads}（上限 ${d.limits.max_threads}）`} />
          {me.data && <KV k="当前账号" v={`${me.data.user.username}（${me.data.user.role === "admin" ? "管理员" : "只读"}）`} />}
          {me.data && <KV k="账号总数" v={formatNumber(me.data.user_count)} />}
        </CardBody>
      </Card>

      <Card>
        <CardHeader action={<Cpu className="h-3.5 w-3.5 text-muted-foreground" />}>
          <CardTitle hint={d.probe.note || undefined}>能力检测</CardTitle>
        </CardHeader>
        <CardBody className="grid gap-2 sm:grid-cols-2">
          {capList.map((c) => (
            <div
              key={c.key}
              className={cn(
                "flex items-start gap-2 rounded-md border px-3 py-2",
                c.on ? "border-success/25 bg-success/[0.05]" : "border-border bg-muted/30",
              )}
            >
              <span className={cn("mt-0.5", c.on ? "text-success" : "text-muted-foreground")}>
                {c.on ? <CheckCircle2 className="h-3.5 w-3.5" /> : <Info className="h-3.5 w-3.5" />}
              </span>
              <div className="min-w-0">
                <div className="text-[12.5px] font-medium">
                  {c.label}
                  <span className={cn("ml-1.5 text-[11px]", c.on ? "text-success" : "text-muted-foreground")}>
                    {c.on ? "可用" : "不可用"}
                  </span>
                </div>
                <div className="text-[11px] text-muted-foreground">{c.note}</div>
              </div>
            </div>
          ))}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>部署提示</CardTitle>
        </CardHeader>
        <CardBody className="space-y-2 text-[12px] leading-relaxed text-muted-foreground">
          <p>
            · 群晖上推荐用 Docker 部署，把媒体目录以只读方式挂载进容器（<code className="mono">/volume1/photo:/media/photo:ro</code>），
            隔离区目录单独挂载可写卷。这样即使程序出错也删不到原始媒体。
          </p>
          <p>
            · 没有 ffmpeg 时，完全重复与相似图片检测完全不受影响 —— 两者都是纯 Go 实现。只有视频关键帧相似度需要 ffmpeg。
          </p>
          <p>
            · 建议首次运行先用「极速」预设在少量目录上验证索引链路，再逐步扩大范围。
          </p>
          {d.capabilities.ffmpeg ? null : (
            <p className="text-warning">
              · 当前环境未检测到 ffmpeg。如需视频相似度，请在容器内安装 ffmpeg 后重启。
            </p>
          )}
        </CardBody>
      </Card>
    </div>
  );
}

function KV({ k, v, mono }: { k: string; v: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline gap-2">
      <span className="shrink-0 text-muted-foreground">{k}</span>
      <span className={cn("min-w-0 break-all", mono && "font-mono text-[11px]")} title={v}>
        {v}
      </span>
    </div>
  );
}
