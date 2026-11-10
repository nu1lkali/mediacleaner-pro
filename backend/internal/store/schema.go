package store

// schemaSQL 是完整的库结构定义（幂等，全部 CREATE ... IF NOT EXISTS）。
// 与 docs/ARCHITECTURE.md 第 4 章一一对应；未来结构变更走 migrate.go 的版本化脚本。
const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS roots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  host_path TEXT NOT NULL UNIQUE,
  real_path TEXT NOT NULL,
  volume TEXT NOT NULL DEFAULT 'unknown',
  writable INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  root_id INTEGER NOT NULL REFERENCES roots(id) ON DELETE CASCADE,
  path TEXT NOT NULL,
  dir_path TEXT NOT NULL,
  name TEXT NOT NULL,
  stem TEXT NOT NULL,
  ext TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_ns INTEGER NOT NULL,
  ctime_ns INTEGER NOT NULL DEFAULT 0,
  -- btime_ns 是「真实创建/出生时间」纳秒；0 表示该文件系统未提供（见 scan/fileinfo_linux.go）。
  -- 与 ctime_ns 严格区分：Linux 的 ctime 是 inode 变更时间，chmod/chown 都会刷新，不能当创建时间用。
  btime_ns INTEGER NOT NULL DEFAULT 0,
  dev INTEGER NOT NULL DEFAULT 0,
  inode INTEGER NOT NULL DEFAULT 0,
  is_symlink INTEGER NOT NULL DEFAULT 0,
  media_kind TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'active',
  hash_status TEXT NOT NULL DEFAULT 'none',
  quick_hash TEXT,
  content_hash TEXT,
  phash TEXT,
  phash_frames TEXT,
  quality_score REAL NOT NULL DEFAULT 0,
  scan_task_id INTEGER,
  last_seen_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_files_path ON files(root_id, path);
CREATE INDEX IF NOT EXISTS ix_files_size ON files(size, media_kind);
CREATE INDEX IF NOT EXISTS ix_files_quick ON files(quick_hash) WHERE quick_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_files_content ON files(content_hash) WHERE content_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_files_stem ON files(root_id, dir_path, stem);
CREATE INDEX IF NOT EXISTS ix_files_kind_state ON files(media_kind, state);
CREATE INDEX IF NOT EXISTS ix_files_ext ON files(ext);
CREATE INDEX IF NOT EXISTS ix_files_scan ON files(scan_task_id);
-- 改名/移动后按 (dev,inode) 继承旧指纹；dev/inode 为 0 的平台不参与，用部分索引省空间
CREATE INDEX IF NOT EXISTS ix_files_inode ON files(dev, inode, size, mtime_ns) WHERE dev <> 0 AND inode <> 0;

CREATE TABLE IF NOT EXISTS file_media (
  file_id INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  video_codec TEXT,
  audio_codec TEXT,
  bitrate INTEGER NOT NULL DEFAULT 0,
  fps REAL NOT NULL DEFAULT 0,
  container TEXT,
  source TEXT,
  extra_json TEXT,
  exif_json TEXT,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS dup_groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  signature TEXT NOT NULL,
  size INTEGER NOT NULL DEFAULT 0,
  member_count INTEGER NOT NULL DEFAULT 0,
  total_bytes INTEGER NOT NULL DEFAULT 0,
  reclaimable_bytes INTEGER NOT NULL DEFAULT 0,
  avg_similarity REAL NOT NULL DEFAULT 0,
  evidence TEXT NOT NULL DEFAULT 'exact',
  -- keeper_rule 是「同组保留条件」：空串=未配置，保持原有去重行为（手动指定/默认路径最浅）。
  -- 非空时，一键应用与执行清理前都会按该条件重新判定组内唯一保留文件。
  keeper_rule TEXT NOT NULL DEFAULT '',
  -- batch_active=1 表示当前界面正在展示的批次。每次扫描单独产出一批分组快照，
  -- 旧批次置 0 归档但仍保留、可在界面切回查看；同一时刻只有一个批次为 1。
  batch_active INTEGER NOT NULL DEFAULT 1,
  -- 产出这批分组的扫描任务。0 = 旧库遗留/未知任务。
  scan_task_id INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
-- 注意：批次相关的两个索引（ux_groups_batch / ix_groups_batch）不在这里创建。
-- 它们引用的 batch_active、scan_task_id 对旧库而言是 ensureColumns() 才补的列，
-- 而 schemaSQL 先于迁移执行；写在这里会让旧库在启动第一步就报
-- "apply schema: no such column: batch_active"，新库反而正常，极易漏测。
-- 统一放到 store.ensureIndexes()（在 ensureColumns 之后执行），新库旧库都覆盖。
-- 同一任务内签名唯一；不同任务允许产出完全相同的分组，结果不再互相覆盖。
CREATE INDEX IF NOT EXISTS ix_groups_kind ON dup_groups(kind, reclaimable_bytes DESC);

CREATE TABLE IF NOT EXISTS dup_group_members (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id INTEGER NOT NULL REFERENCES dup_groups(id) ON DELETE CASCADE,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  similarity REAL NOT NULL DEFAULT 1,
  is_keeper INTEGER NOT NULL DEFAULT 0,
  keep_reason TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_member ON dup_group_members(group_id, file_id);
CREATE INDEX IF NOT EXISTS ix_member_file ON dup_group_members(file_id);

CREATE TABLE IF NOT EXISTS scan_tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT 'incremental',
  status TEXT NOT NULL DEFAULT 'pending',
  phase TEXT NOT NULL DEFAULT 'index',
  priority INTEGER NOT NULL DEFAULT 5,
  options_json TEXT NOT NULL DEFAULT '{}',
  root_ids_json TEXT NOT NULL DEFAULT '[]',
  progress_json TEXT NOT NULL DEFAULT '{}',
  current_path TEXT,
  files_seen INTEGER NOT NULL DEFAULT 0,
  files_indexed INTEGER NOT NULL DEFAULT 0,
  bytes_scanned INTEGER NOT NULL DEFAULT 0,
  error_count INTEGER NOT NULL DEFAULT 0,
  queued_at INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER,
  message TEXT
);
CREATE INDEX IF NOT EXISTS ix_scans_status ON scan_tasks(status, priority, queued_at);

CREATE TABLE IF NOT EXISTS scan_checkpoints (
  task_id INTEGER NOT NULL REFERENCES scan_tasks(id) ON DELETE CASCADE,
  root_id INTEGER NOT NULL,
  cursor_dir TEXT,
  phase TEXT,
  done INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (task_id, root_id)
);

CREATE TABLE IF NOT EXISTS scan_errors (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL,
  path TEXT,
  code TEXT,
  message TEXT,
  ts INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_scanerr_task ON scan_errors(task_id);

CREATE TABLE IF NOT EXISTS rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  priority INTEGER NOT NULL DEFAULT 100,
  auto_execute INTEGER NOT NULL DEFAULT 0,
  require_confirmation INTEGER NOT NULL DEFAULT 1,
  scope_json TEXT NOT NULL DEFAULT '{}',
  match_json TEXT NOT NULL DEFAULT '{}',
  retention_json TEXT NOT NULL DEFAULT '{}',
  delete_json TEXT NOT NULL DEFAULT '{}',
  associated_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS clean_tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  rule_id INTEGER,
  status TEXT NOT NULL DEFAULT 'draft',
  execute_mode TEXT NOT NULL DEFAULT 'quarantine',
  stats_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER,
  message TEXT
);

CREATE TABLE IF NOT EXISTS clean_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES clean_tasks(id) ON DELETE CASCADE,
  group_id INTEGER,
  file_id INTEGER,
  path TEXT NOT NULL,
  name TEXT NOT NULL,
  ext TEXT NOT NULL,
  size INTEGER NOT NULL,
  media_kind TEXT NOT NULL,
  reason TEXT,
  rule_id INTEGER,
  hit_rules TEXT,
  keeper_path TEXT,
  assoc_of INTEGER,
  action TEXT NOT NULL DEFAULT 'pending',
  error TEXT,
  quarantine_id INTEGER,
  executed_at INTEGER,
  selected INTEGER NOT NULL DEFAULT 1,
  expected_mtime_ns INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_citems_task ON clean_items(task_id, action);
CREATE INDEX IF NOT EXISTS ix_citems_path ON clean_items(path);

CREATE TABLE IF NOT EXISTS quarantine_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  orig_path TEXT NOT NULL,
  root_id INTEGER,
  stored_path TEXT NOT NULL,
  size INTEGER NOT NULL,
  -- sha256 存「隔离时用来命名去重的那个值」，具体算法见 hash_algo：
  --   blake3 = 复用了扫描/清理前复验算出的 BLAKE3（隔离时零读盘）
  --   sha256 = 现场算的 SHA-256（没有可用哈希时的兜底）
  --   none   = 完全免校验模式，用 mtime/size 派生键，不做内容去重
  sha256 TEXT NOT NULL,
  hash_algo TEXT NOT NULL DEFAULT 'sha256',
  media_kind TEXT,
  rule_id INTEGER,
  clean_task_id INTEGER,
  status TEXT NOT NULL DEFAULT 'quarantined',
  quarantined_at INTEGER NOT NULL,
  expires_at INTEGER,
  restored_at INTEGER,
  message TEXT
);
CREATE INDEX IF NOT EXISTS ix_q_status ON quarantine_items(status, expires_at);
CREATE INDEX IF NOT EXISTS ix_q_orig ON quarantine_items(orig_path);

CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  actor TEXT,
  action TEXT NOT NULL,
  target_type TEXT,
  target_id TEXT,
  detail_json TEXT,
  ip TEXT
);
CREATE INDEX IF NOT EXISTS ix_audit_ts ON audit_log(ts DESC);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS schedules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'scan',
  freq TEXT NOT NULL,
  at_time TEXT,
  weekday INTEGER,
  monthday INTEGER,
  root_ids_json TEXT NOT NULL DEFAULT '[]',
  options_json TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1,
  last_run_at INTEGER,
  next_run_at INTEGER
);

CREATE TABLE IF NOT EXISTS export_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL DEFAULT 'clean',
  fmt TEXT NOT NULL DEFAULT 'csv',
  scope_json TEXT NOT NULL DEFAULT '{}',
  file_path TEXT NOT NULL DEFAULT '',
  rows INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'ready',
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'admin',
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_login_at INTEGER
);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ip TEXT,
  user_agent TEXT
);
CREATE INDEX IF NOT EXISTS ix_sessions_exp ON sessions(expires_at);
`
