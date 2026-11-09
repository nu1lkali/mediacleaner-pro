import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatBytes(n: number | null | undefined, digits = 1): string {
  if (n === null || n === undefined || Number.isNaN(n)) return "—";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let v = Number(n);
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return i === 0 ? `${Math.round(v)} B` : `${v.toFixed(digits)} ${units[i]}`;
}

export function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  return new Intl.NumberFormat("zh-CN").format(n);
}

export function formatDate(ms: number | null | undefined, withTime = true): string {
  if (!ms) return "—";
  const d = new Date(ms);
  const pad = (x: number) => String(x).padStart(2, "0");
  const base = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return withTime ? `${base} ${pad(d.getHours())}:${pad(d.getMinutes())}` : base;
}

export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return "—";
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

export function formatSpeed(bps: number | null | undefined): string {
  if (!bps) return "—";
  return `${formatBytes(bps)}/s`;
}

export function percent(part: number, total: number): number {
  if (!total) return 0;
  return Math.min(100, Math.max(0, (part / total) * 100));
}

export const statusText: Record<string, string> = {
  pending: "排队中",
  running: "执行中",
  paused: "已暂停",
  cancelling: "取消中",
  succeeded: "已完成",
  completed: "已完成",
  failed: "失败",
  canceled: "已取消",
  cancelled: "已取消",
  preview: "待确认",
  partial: "部分完成",
  draft: "待确认",
  quarantined: "已隔离",
  restored: "已恢复",
  purged: "已永久删除",
  expired: "已过期",
  skipped: "已跳过",
  deleted: "已永久删除",
  pending_action: "待执行",
};

export const phaseText: Record<string, string> = {
  queued: "排队中",
  enumerate: "枚举目录",
  index: "建立元数据索引",
  probe: "读取媒体信息",
  quick: "快速指纹",
  exact: "精确校验",
  similar: "相似检测",
  "similar·image": "图片相似度分析",
  "similar·video": "视频关键帧分析",
  group: "生成分组",
  done: "完成",
};

export const actionText: Record<string, string> = {
  pending: "待执行",
  quarantined: "已移入隔离区",
  deleted: "已永久删除",
  failed: "失败",
  skipped: "已跳过",
};

export function statusTone(status: string): "default" | "success" | "warn" | "danger" | "info" {
  switch (status) {
    case "completed":
    case "succeeded":
    case "ready":
    case "ready_to_clean":
      return "success";
    case "running":
    case "pending":
      return "info";
    case "paused":
    case "preview":
    case "partial":
    case "draft":
      return "warn";
    case "failed":
    case "cancelled":
    case "canceled":
      return "danger";
    default:
      return "default";
  }
}

export function kindLabel(kind: string): string {
  return { video: "视频", image: "图片", audio: "音频", document: "文档", other: "其他" }[kind] || kind;
}

export function groupKindLabel(kind: string): string {
  return { exact: "完全重复", similar_video: "相似视频", similar_image: "相似图片", video: "相似视频", image: "相似图片", name: "同名不同后缀" }[kind] || kind;
}

/** 720p/1080p 之类的分辨率标签。 */
export function resolutionLabel(width?: number, height?: number): string {
  if (!width || !height) return "—";
  const px = width * height;
  if (px >= 3840 * 2160) return "4K";
  if (px >= 2560 * 1440) return "2K";
  if (px >= 1920 * 1080) return "1080P";
  if (px >= 1280 * 720) return "720P";
  if (px >= 640 * 480) return "480P";
  return `${width}×${height}`;
}

/** 毫秒 → 01:23:45 */
export function formatClock(ms: number | null | undefined): string {
  if (!ms || ms < 0) return "—";
  const total = Math.floor(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const pad = (x: number) => String(x).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}

