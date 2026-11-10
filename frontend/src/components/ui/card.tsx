import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "@/lib/utils";

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("card", className)} {...props} />;
}

export function CardHeader({ className, children, action }: { className?: string; children: ReactNode; action?: ReactNode }) {
  return (
    <div className={cn("flex items-start justify-between gap-3 border-b border-border/70 px-4 py-3", className)}>
      <div className="min-w-0">{children}</div>
      {action}
    </div>
  );
}

export function CardTitle({ children, hint }: { children: ReactNode; hint?: string }) {
  return (
    <div className="min-w-0">
      <h3
        className="truncate text-sm font-semibold text-foreground"
        title={typeof children === "string" ? children : undefined}
      >
        {children}
      </h3>
      {hint ? (
        <p className="mt-0.5 truncate text-xs text-muted-foreground" title={hint}>
          {hint}
        </p>
      ) : null}
    </div>
  );
}

export function CardBody({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("p-4", className)}>{children}</div>;
}

type Tone = "default" | "success" | "warn" | "danger" | "info" | "outline";

const tones: Record<Tone, string> = {
  default: "bg-muted text-muted-foreground border-transparent",
  success: "bg-success/12 text-success border-success/25",
  warn: "bg-warning/12 text-warning border-warning/25",
  danger: "bg-destructive/12 text-destructive border-destructive/25",
  info: "bg-primary/10 text-primary border-primary/25",
  outline: "bg-transparent text-muted-foreground border-border",
};

export function Badge({ tone = "default", children, className }: { tone?: Tone; children: ReactNode; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium leading-4",
        tones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

export function StatCard({
  label,
  value,
  hint,
  icon,
  tone = "default",
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  icon?: ReactNode;
  tone?: "default" | "primary" | "success" | "warn" | "danger";
}) {
  const ring: Record<string, string> = {
    default: "text-muted-foreground bg-muted",
    primary: "text-primary bg-primary/10",
    success: "text-success bg-success/10",
    warn: "text-warning bg-warning/10",
    danger: "text-destructive bg-destructive/10",
  };
  return (
    <Card className="p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-xs font-medium text-muted-foreground">{label}</div>
          <div className="mt-1.5 truncate text-2xl font-semibold tabular-nums tracking-tight">{value}</div>
          {hint ? <div className="mt-1 truncate text-xs text-muted-foreground">{hint}</div> : null}
        </div>
        {icon ? <div className={cn("rounded-md p-2", ring[tone])}>{icon}</div> : null}
      </div>
    </Card>
  );
}
