/** 与 Go 后端 API 一一对应的类型定义。字段名保持后端 JSON 原名（snake_case）。 */

export type MediaKind = "image" | "video" | "audio" | "document" | "other";
export type GroupKind = "exact" | "image" | "video" | "name";
export type ScanStatus =
  | "pending"
  | "running"
  | "paused"
  | "succeeded"
  | "failed"
  | "canceled";

export interface User {
  id: number;
  username: string;
  role: "admin" | "viewer";
  disabled: boolean;
  created_at: number;
  last_login_at: number | null;
}

export interface Capabilities {
  ffmpeg: boolean;
  ffprobe: boolean;
  video_thumbnail: boolean;
  video_transcode: boolean;
  video_similarity: boolean;
  native_probe: boolean;
  image_probe: boolean;
  image_similarity: boolean;
}

export interface Bootstrap {
  app: string;
  version: string;
  initialized: boolean;
  capabilities: Capabilities;
}

export interface LoginResult {
  user: User;
  expires_at: number;
  capabilities: Capabilities;
}

export interface MeResult {
  user: User;
  user_count: number;
  capabilities: Capabilities;
  app: string;
  version: string;
}

export interface Root {
  id: number;
  name: string;
  host_path: string;
  real_path: string;
  volume: string;
  writable: boolean;
  enabled: boolean;
  created_at: number;
  file_count: number;
  total_size: number;
}

export interface BrowseEntry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
}

export interface BrowseResult {
  path: string;
  parent: string;
  root: string;
  /** 可浏览的根目录（已注册存储卷 + 白名单目录），用于选择器的快捷入口 */
  roots?: { name: string; path: string }[];
  dirs: BrowseEntry[];
  file_count: number;
}

export interface ScanOptions {
  mode: "full" | "incremental";
  profile: "fast" | "standard" | "full";
  root_ids: number[];
  detect_exact: boolean;
  detect_similar_image: boolean;
  detect_similar_video: boolean;
  only_kinds: string[];
  min_file_size: number;
  exclude_dirs: string[];
  exclude_names: string[];
  max_depth: number;
  follow_symlinks: boolean;
  include_other: boolean;
  deep_probe: boolean;
  threads: number;
  limit_mbps: number;
}

export interface ScanTask {
  id: number;
  name: string;
  mode: string;
  status: ScanStatus;
  phase: string;
  phase_label: string;
  priority: number;
  options: ScanOptions;
  root_ids: number[];
  progress: Record<string, unknown>;
  current_path: string;
  files_seen: number;
  files_indexed: number;
  bytes_scanned: number;
  error_count: number;
  queued_at: number;
  started_at: number | null;
  finished_at: number | null;
  message: string;
}

export interface ScanProgress {
  task_id: number;
  status: ScanStatus;
  phase: string;
  phase_label: string;
  current_path: string;
  files_seen: number;
  files_indexed: number;
  bytes_scanned: number;
  error_count: number;
  phase_done: number;
  phase_total: number;
  percent: number;
  elapsed_ms: number;
  message: string;
}

export interface ScanProfile {
  id: "fast" | "standard" | "full";
  name: string;
  desc: string;
  detect_exact: boolean;
  detect_image: boolean;
  detect_video: boolean;
  need_ffmpeg: boolean;
}

export interface ScanErrorRow {
  path: string;
  code: string;
  message: string;
  ts: number;
}

export interface GroupMember {
  file_id: number;
  path: string;
  name: string;
  ext: string;
  size: number;
  media_kind: MediaKind;
  mtime_ns: number;
  similarity: number;
  is_keeper: boolean;
  keep_reason: string;
  volume: string;
  root_id: number;
  width: number;
  height: number;
  duration_ms: number;
  bitrate: number;
  video_codec: string;
  audio_codec: string;
  hash_status: string;
  state: string;
}

export interface DupGroup {
  id: number;
  kind: GroupKind;
  signature: string;
  size: number;
  member_count: number;
  total_bytes: number;
  reclaimable_bytes: number;
  avg_similarity: number;
  created_at: number;
  members: GroupMember[];
}

export interface GroupSummaryItem {
  kind: GroupKind;
  groups: number;
  removable: number;
  bytes: number;
}

export interface GroupSummary {
  items: GroupSummaryItem[];
  total_groups: number;
  removable_files: number;
  reclaimable: number;
}

export interface MediaInfo {
  DurationMS: number;
  Width: number;
  Height: number;
  VideoCodec: string;
  AudioCodec: string;
  Bitrate: number;
  FPS: number;
  Container: string;
  Source: string;
  ExtraJSON: string;
  ExifJSON: string;
}

export interface FileDetail {
  id: number;
  root_id: number;
  path: string;
  dir_path: string;
  name: string;
  stem: string;
  ext: string;
  size: number;
  mtime_ns: number;
  media_kind: MediaKind;
  state: string;
  hash_status: string;
  quick_hash: string;
  content_hash: string;
  phash: string;
  volume: string;
  root_name: string;
  media: MediaInfo | null;
  extra_json: string;
  exif_json: string;
  needs_proxy: boolean;
  playable: boolean;
  groups: { group_id: number; kind: GroupKind; members: number; similarity: number; is_keeper: boolean }[];
}

/* ---------- 规则 ---------- */

export interface RuleScope {
  root_ids: number[];
  volumes: string[];
  dirs: string[];
  exclude_dirs: string[];
  kinds: string[];
  ext_include: string[];
  ext_exclude: string[];
  min_size: number;
  max_size: number;
  name_include: string[];
  name_exclude: string[];
  name_regex: string;
}

export interface SameNameMatch {
  trigger_extensions: string[];
  associated_extensions: string[];
  same_directory_only: boolean;
  same_basename_only: boolean;
  protect_referenced: boolean;
}

export interface RuleMatch {
  group_kinds: GroupKind[];
  min_similarity: number;
  min_group_members: number;
  require_exact_content: boolean;
  stale_days: number;
  operator: "and" | "or";
  same_name: SameNameMatch | null;
}

export interface RetentionKey {
  field: string;
  order: "asc" | "desc";
  weight: number;
  value: string;
  prefer: string[];
}

export interface RuleRetention {
  strategy: string;
  keys: RetentionKey[];
  keep_count: number;
  pinned_paths: string[];
}

export interface RuleDelete {
  kinds: string[];
  max_resolution: number;
  min_resolution: number;
  older_than_days: number;
  path_regex: string[];
  max_deletes_per_run: number;
  min_reclaim_bytes: number;
  keep_at_least_one: boolean;
}

export interface RuleAssociated {
  enabled: boolean;
  trigger_extensions: string[];
  associated_extensions: string[];
  same_directory_only: boolean;
  same_basename_only: boolean;
  require_confirmation: boolean;
  protect_referenced: boolean;
}

export interface Rule {
  id: number;
  name: string;
  description: string;
  enabled: boolean;
  priority: number;
  auto_execute: boolean;
  require_confirmation: boolean;
  scope: RuleScope;
  match: RuleMatch;
  retention: RuleRetention;
  delete: RuleDelete;
  associated: RuleAssociated;
  created_at: number;
  updated_at: number;
}

/* ---------- 清理 ---------- */

export interface PlanItem {
  group_id: number;
  file_id: number;
  path: string;
  name: string;
  ext: string;
  size: number;
  media_kind: MediaKind;
  reason: string;
  rule_id: number;
  rule_name: string;
  keeper_path: string;
  assoc_of: string;
  is_assoc: boolean;
  expected_mtime_ns: number;
  root_id: number;
  similarity: number;
  volume: string;
  hit_rules?: string[];
}

export interface DryRunResult {
  items: PlanItem[];
  total_items: number;
  truncated: boolean;
  group_count: number;
  total_bytes: number;
  by_kind: Record<string, number>;
  by_kind_bytes: Record<string, number>;
  rule_stats: { rule_id: number; rule_name: string; items: number; bytes: number; groups: number }[];
  notes: string[];
}

export interface CleanTask {
  id: number;
  name: string;
  rule_id: number;
  status: "draft" | "running" | "succeeded" | "failed" | "canceled";
  execute_mode: "quarantine" | "permanent";
  stats: Record<string, unknown>;
  created_at: number;
  started_at: number | null;
  finished_at: number | null;
  message: string;
}

export interface CleanItem {
  id: number;
  task_id: number;
  group_id: number;
  file_id: number;
  path: string;
  name: string;
  ext: string;
  size: number;
  media_kind: MediaKind;
  reason: string;
  rule_id: number;
  hit_rules: string;
  keeper_path: string;
  assoc_of: number;
  action: string;
  error: string;
  quarantine_id: number;
  executed_at: number | null;
  selected: boolean;
  expected_mtime_ns: number;
}

export interface CleanItemsResult {
  items: CleanItem[];
  total: number;
  selected: number;
  total_bytes: number;
  selected_bytes: number;
  by_kind: Record<string, { count: number; bytes: number }>;
}

export interface QuarantineItem {
  id: number;
  orig_path: string;
  root_id: number;
  stored_path: string;
  size: number;
  sha256: string;
  media_kind: string;
  rule_id: number;
  clean_task_id: number;
  status: string;
  quarantined_at: number;
  expires_at: number;
  restored_at: number | null;
  message: string;
}

/* ---------- 统计 ---------- */

export interface KindStat {
  kind: MediaKind;
  files: number;
  bytes: number;
}

export interface DashboardData {
  files: {
    total: number;
    bytes: number;
    by_kind: KindStat[];
    videos: number;
    images: number;
    audio: number;
    docs: number;
  };
  duplicates: {
    exact_groups: number;
    exact_files: number;
    exact_bytes: number;
    image_groups: number;
    image_files: number;
    image_bytes: number;
    video_groups: number;
    video_files: number;
    video_bytes: number;
    reclaimable: number;
  };
  cleaned: { files: number; bytes: number };
  quarantine: { count: number; bytes: number };
  roots: Root[];
  recent_scans: {
    id: number;
    name: string;
    status: ScanStatus;
    phase: string;
    files_seen: number;
    bytes_scanned: number;
    error_count: number;
    queued_at: number;
    finished_at: number | null;
  }[];
  recent_cleans: {
    id: number;
    name: string;
    status: string;
    mode: string;
    stats: Record<string, unknown>;
    created_at: number;
    message: string;
  }[];
}

export interface LibraryStats {
  by_ext: { ext: string; files: number; bytes: number }[];
  by_resolution: { bucket: string; files: number; bytes: number }[];
  by_root: { name: string; volume: string; files: number; bytes: number }[];
}

export interface Suggestion {
  key: string;
  title: string;
  detail: string;
  files: number;
  bytes: number;
  severity: "info" | "warn" | "ok";
}

export interface SuggestionsResult {
  items: Suggestion[];
  top_dirs: { dir: string; files: number; bytes: number }[];
}

export interface SystemInfo {
  app: string;
  version: string;
  home: string;
  data_root: string;
  capabilities: Capabilities;
  probe: { ffprobe: boolean; ffmpeg: boolean; mode: string; note: string };
  indexed_files: number;
  indexed_bytes: number;
  groups: number;
  quarantine: { count: number; bytes: number };
  threads: number;
  limits: { max_threads: number; batch_size: number };
}

export interface SettingsPayload {
  values: Record<string, unknown>;
  schema: Record<string, string>;
  defaults: Record<string, unknown>;
  runtime: {
    version: string;
    data_root: string;
    cache_dir: string;
    db_path: string;
    threads: number;
    max_threads: number;
    capabilities: Capabilities;
    probe: { ffprobe: boolean; ffmpeg: boolean; mode: string; note: string };
  };
}

export interface Page<T> {
  items: T[];
  total: number;
  page?: number;
  size?: number;
}
