import { cn } from "@/lib/utils";

const tones = {
  primary: "bg-primary",
  success: "bg-success",
  warn: "bg-warning",
  danger: "bg-destructive",
};

export function Progress({
  value,
  tone = "primary",
  className,
  height = 6,
  animated,
}: {
  value: number;
  tone?: keyof typeof tones;
  className?: string;
  height?: number;
  animated?: boolean;
}) {
  const v = Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0));
  return (
    <div
      className={cn("w-full overflow-hidden rounded-full bg-muted", className)}
      style={{ height }}
      role="progressbar"
      aria-valuenow={Math.round(v)}
      aria-valuemin={0}
      aria-valuemax={100}
    >
      <div
        className={cn("h-full rounded-full transition-all duration-500", tones[tone], animated && "relative")}
        style={{ width: `${v}%` }}
      />
    </div>
  );
}

/** 环形百分比（用于扫描详情等强调场景）。 */
export function Ring({ value, size = 64, stroke = 6, children }: {
  value: number;
  size?: number;
  stroke?: number;
  children?: React.ReactNode;
}) {
  const v = Math.max(0, Math.min(100, value || 0));
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  return (
    <div className="relative inline-flex items-center justify-center" style={{ width: size, height: size }}>
      <svg width={size} height={size} className="-rotate-90">
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" strokeWidth={stroke} className="stroke-muted" />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          strokeWidth={stroke}
          strokeLinecap="round"
          strokeDasharray={c}
          strokeDashoffset={c - (c * v) / 100}
          className="stroke-primary transition-all duration-500"
        />
      </svg>
      <div className="absolute inset-0 flex items-center justify-center text-[11px] font-semibold tabular-nums">
        {children ?? `${Math.round(v)}%`}
      </div>
    </div>
  );
}
