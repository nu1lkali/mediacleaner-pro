import type {
  Bootstrap,
  BrowseResult,
  CleanItem,
  CleanItemsResult,
  CleanTask,
  DashboardData,
  DryRunResult,
  DupGroup,
  FileDetail,
  GroupKind,
  GroupSummary,
  LibraryStats,
  LoginResult,
  MeResult,
  Page,
  QuarantineItem,
  Root,
  Rule,
  ScanErrorRow,
  ScanProfile,
  ScanTask,
  SettingsPayload,
  SuggestionsResult,
  SystemInfo,
  User,
} from "./types";

const BASE = "/api";

export class ApiError extends Error {
  status: number;
  code?: string;
  constructor(message: string, status: number, code?: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let onUnauthorized: (() => void) | null = null;
/** 注册 401 回调：路由层用它跳转到登录页。 */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = { ...(init.headers as Record<string, string>) };
  if (init.body && !headers["Content-Type"]) headers["Content-Type"] = "application/json";

  let res: Response;
  try {
    res = await fetch(`${BASE}${path}`, {
      credentials: "same-origin",
      ...init,
      headers,
    });
  } catch {
    throw new ApiError("无法连接服务器，请检查服务是否运行", 0, "network_error");
  }

  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }

  if (!res.ok) {
    const err = data as { error?: string; code?: string } | null;
    const msg =
      (typeof err?.error === "string" && err.error) || `请求失败 (${res.status})`;
    if (res.status === 401 && onUnauthorized) onUnauthorized();
    throw new ApiError(msg, res.status, err?.code);
  }
  return data as T;
}

const qs = (params: Record<string, unknown>) => {
  const sp = new URLSearchParams();
  Object.entries(params).forEach(([k, v]) => {
    if (v === undefined || v === null || v === "" || v === "all") return;
    sp.set(k, String(v));
  });
  const s = sp.toString();
  return s ? `?${s}` : "";
};

const json = (body: unknown): RequestInit => ({ body: JSON.stringify(body) });

export const api = {
  /* ---------- 认证 ---------- */
  bootstrap: () => request<Bootstrap>("/system/bootstrap"),
  login: (username: string, password: string, remember = true) =>
    request<LoginResult>("/auth/login", { method: "POST", ...json({ username, password, remember }) }),
  logout: () => request<{ ok: boolean }>("/auth/logout", { method: "POST" }),
  me: () => request<MeResult>("/auth/me"),
  changePassword: (old_password: string, new_password: string) =>
    request<{ ok: boolean; message: string }>("/auth/password", {
      method: "POST",
      ...json({ old_password, new_password }),
    }),

  /* ---------- 用户 ---------- */
  users: () => request<{ items: User[] }>("/users"),
  createUser: (username: string, password: string, role: "admin" | "viewer") =>
    request<User>("/users", { method: "POST", ...json({ username, password, role }) }),
  deleteUser: (id: number) => request<{ ok: boolean }>(`/users/${id}`, { method: "DELETE" }),

  /* ---------- 系统 ---------- */
  systemInfo: () => request<SystemInfo>("/system/info"),

  /* ---------- 存储卷 ---------- */
  roots: () => request<{ items: Root[]; data_root: string }>("/roots"),
  addRoot: (name: string, path: string, writable = false) =>
    request<Root>("/roots", { method: "POST", ...json({ name, path, writable }) }),
  updateRoot: (id: number, body: Partial<{ name: string; enabled: boolean; writable: boolean }>) =>
    request<Root>(`/roots/${id}`, { method: "PATCH", ...json(body) }),
  deleteRoot: (id: number) =>
    request<{ ok: boolean; note: string }>(`/roots/${id}`, { method: "DELETE" }),
  browse: (path?: string) => request<BrowseResult>(`/roots/browse${qs({ path })}`),

  /* ---------- 扫描 ---------- */
  scans: (limit = 50, offset = 0) =>
    request<{ items: ScanTask[]; total: number }>(`/scans${qs({ limit, offset })}`),
  scan: (id: number) => request<ScanTask>(`/scans/${id}`),
  currentScan: () => request<{ task: ScanTask | null }>("/scans/current"),
  scanProfiles: () =>
    request<{ items: ScanProfile[]; capabilities: Record<string, boolean> }>("/scans/profiles"),
  createScan: (body: Record<string, unknown>) =>
    request<ScanTask>("/scans", { method: "POST", ...json(body) }),
  controlScan: (id: number, action: "pause" | "resume" | "cancel") =>
    request<ScanTask>(`/scans/${id}/control`, { method: "POST", ...json({ action }) }),
  scanErrors: (id: number, limit = 200) =>
    request<{ items: ScanErrorRow[] }>(`/scans/${id}/errors${qs({ limit })}`),
  deleteScan: (id: number) => request<{ ok: boolean }>(`/scans/${id}`, { method: "DELETE" }),
  scanEventsUrl: (id: number) => `${BASE}/scans/${id}/events`,

  /* ---------- 重复组 ---------- */
  groups: (params: {
    kind: GroupKind;
    page?: number;
    size?: number;
    sort?: string;
    min_reclaim?: number;
    volume?: string;
  }) => request<{ items: DupGroup[]; total: number; page: number; size: number }>(`/groups${qs(params)}`),
  group: (id: number) => request<DupGroup>(`/groups/${id}`),
  groupSummary: () => request<GroupSummary>("/groups/summary"),
  setKeeper: (groupId: number, fileId: number) =>
    request<DupGroup>(`/groups/${groupId}/keeper`, { method: "POST", ...json({ file_id: fileId }) }),

  /* ---------- 文件与媒体 ---------- */
  file: (id: number) => request<FileDetail>(`/files/${id}`),
  thumbUrl: (id: number, w = 360) => `${BASE}/files/${id}/thumb?w=${w}`,
  rawUrl: (id: number) => `${BASE}/files/${id}/raw`,
  proxyUrl: (id: number) => `${BASE}/files/${id}/proxy`,

  /* ---------- 规则 ---------- */
  rules: () => request<{ items: Rule[] }>("/rules"),
  rule: (id: number) => request<Rule>(`/rules/${id}`),
  createRule: (body: Omit<Rule, "id" | "created_at" | "updated_at">) =>
    request<Rule>("/rules", { method: "POST", ...json(body) }),
  updateRule: (id: number, body: Rule) => request<Rule>(`/rules/${id}`, { method: "PUT", ...json(body) }),
  deleteRule: (id: number) => request<{ ok: boolean }>(`/rules/${id}`, { method: "DELETE" }),
  toggleRule: (id: number, enabled: boolean) =>
    request<Rule>(`/rules/${id}/toggle`, { method: "POST", ...json({ enabled }) }),
  dryRun: (rule_ids: number[], max_items = 2000) =>
    request<DryRunResult>("/rules/dry-run", { method: "POST", ...json({ rule_ids, max_items }) }),

  /* ---------- 清理 ---------- */
  cleanTasks: (limit = 50, offset = 0) =>
    request<{ items: CleanTask[]; total: number }>(`/clean/tasks${qs({ limit, offset })}`),
  cleanTask: (id: number) => request<CleanTask>(`/clean/tasks/${id}`),
  cleanItems: (id: number) => request<CleanItemsResult>(`/clean/tasks/${id}/items`),
  createCleanTask: (body: {
    name?: string;
    rule_ids?: number[];
    file_ids?: number[];
    execute_mode?: "quarantine" | "permanent";
  }) => request<CleanTask>("/clean/tasks", { method: "POST", ...json(body) }),
  executeClean: (id: number) =>
    request<{ ok: boolean; message: string }>(`/clean/tasks/${id}/execute`, { method: "POST" }),
  cancelClean: (id: number) =>
    request<{ ok: boolean }>(`/clean/tasks/${id}/cancel`, { method: "POST" }),
  deleteCleanTask: (id: number) => request<{ ok: boolean }>(`/clean/tasks/${id}`, { method: "DELETE" }),
  selectItem: (itemId: number, selected: boolean) =>
    request<{ ok: boolean }>(`/clean/items/${itemId}`, { method: "PATCH", ...json({ selected }) }),

  /* ---------- 隔离区 ---------- */
  quarantine: (status = "quarantined", limit = 50, offset = 0) =>
    request<{
      items: QuarantineItem[];
      total: number;
      quarantined_count: number;
      quarantined_bytes: number;
      retention_days: number;
    }>(`/quarantine${qs({ status, limit, offset })}`),
  quarantineItem: (id: number) => request<QuarantineItem>(`/quarantine/${id}`),
  restore: (id: number) => request<{ ok: boolean }>(`/quarantine/${id}/restore`, { method: "POST" }),
  purge: (id: number) => request<{ ok: boolean }>(`/quarantine/${id}`, { method: "DELETE" }),
  purgeExpired: () =>
    request<{ ok: boolean; purged: number }>("/quarantine/purge-expired", { method: "POST" }),

  /* ---------- 统计 ---------- */
  dashboard: () => request<DashboardData>("/stats/dashboard"),
  libraryStats: () => request<LibraryStats>("/stats/library"),
  suggestions: () => request<SuggestionsResult>("/stats/suggestions"),

  /* ---------- 导出 ---------- */
  exportData: (body: {
    kind: "clean" | "groups" | "scan";
    format: "csv" | "xlsx" | "json" | "html";
    clean_task_id?: number;
    scan_task_id?: number;
    kind_filter?: string;
  }) =>
    request<{ file: string; path: string; rows: number; download_url: string }>("/export", {
      method: "POST",
      ...json(body),
    }),
  downloadUrl: (url: string) => (url.startsWith("/api") ? url : `${BASE}${url}`),

  /* ---------- 设置 ---------- */
  settings: () => request<SettingsPayload>("/settings"),
  saveSettings: (values: Record<string, unknown>) =>
    request<{ ok: boolean; applied: string[] }>("/settings", { method: "PUT", ...json(values) }),
};

export type { CleanItem };
