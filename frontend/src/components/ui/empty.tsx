import type { ReactNode } from "react";
import { Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";

export function PageHeader({
  title,
  desc,
  actions,
  className,
}: {
  title: string;
  desc?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-wrap items-end justify-between gap-3 px-4 sm:px-5 pb-4 pt-5", className)}>
      <div className="min-w-0">
        <h1 className="text-[17px] font-semibold tracking-tight">{title}</h1>
        {desc ? <p className="mt-1 text-[12.5px] leading-relaxed text-muted-foreground">{desc}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}

export function EmptyState({
  icon,
  title,
  desc,
  action,
  className,
}: {
  icon?: ReactNode;
  title: string;
  desc?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-center justify-center px-6 py-14 text-center", className)}>
      {icon ? (
        <div className="mb-3 flex h-11 w-11 items-center justify-center rounded-full bg-muted text-muted-foreground">
          {icon}
        </div>
      ) : null}
      <div className="text-sm font-medium text-foreground">{title}</div>
      {desc ? (
        <div className="mt-1 max-w-md text-[12.5px] leading-relaxed text-muted-foreground">{desc}</div>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  );
}

export function Loading({ label = "加载中…", className }: { label?: string; className?: string }) {
  return (
    <div className={cn("flex items-center justify-center gap-2 py-12 text-[13px] text-muted-foreground", className)}>
      <Loader2 className="h-4 w-4 animate-spin" />
      {label}
    </div>
  );
}

export function ErrorBox({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="m-4 rounded-lg border border-destructive/30 bg-destructive/8 px-4 py-3 text-[13px] text-destructive">
      <div className="font-medium">出错了</div>
      <div className="mt-0.5 text-destructive/90">{message}</div>
      {onRetry ? (
        <button onClick={onRetry} className="mt-2 rounded border border-destructive/40 px-2 py-1 text-xs hover:bg-destructive/10">
          重试
        </button>
      ) : null}
    </div>
  );
}

export function Section({ title, desc, right, children, className }: {
  title?: string;
  desc?: string;
  right?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={className}>
      {title ? (
        <div className="mb-2 flex items-end justify-between gap-3">
          <div>
            <h2 className="text-[13px] font-semibold text-foreground">{title}</h2>
            {desc ? <p className="mt-0.5 text-[11.5px] text-muted-foreground">{desc}</p> : null}
          </div>
          {right}
        </div>
      ) : null}
      {children}
    </section>
  );
}
