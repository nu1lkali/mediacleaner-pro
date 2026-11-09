import { useEffect } from "react";
import type { ReactNode } from "react";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";

export function Dialog({
  open,
  onClose,
  title,
  desc,
  children,
  footer,
  width = "max-w-lg",
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  desc?: ReactNode;
  children?: ReactNode;
  footer?: ReactNode;
  width?: string;
}) {
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [open, onClose]);

  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center bg-black/40 backdrop-blur-[1px] sm:items-start sm:overflow-y-auto sm:px-4 sm:py-10">
      <div
        className={cn(
          "w-full animate-fade-in rounded-t-2xl border border-border bg-card shadow-lift sm:rounded-xl",
          // 手机上贴底弹出，占满宽度；桌面维持居中卡片
          "max-h-[92dvh] pb-[env(safe-area-inset-bottom)] sm:max-h-none sm:pb-0",
          width,
        )}
        role="dialog"
        aria-modal="true"
      >
        {/* 移动端拖拽提示条 */}
        <div className="mx-auto mt-2 h-1 w-9 rounded-full bg-border sm:hidden" />
        <div className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <h3 className="text-sm font-semibold">{title}</h3>
            {desc ? <div className="mt-0.5 text-[12px] text-muted-foreground">{desc}</div> : null}
          </div>
          <button
            onClick={onClose}
            className="-mr-1 flex h-7 w-7 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
            aria-label="关闭"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className="max-h-[68dvh] overflow-y-auto overscroll-y-contain px-4 py-4 sm:max-h-[70vh]">
          {children}
        </div>
        {footer ? (
          <div className="flex flex-col-reverse gap-2 border-t border-border px-4 py-3 sm:flex-row sm:items-center sm:justify-end [&>a]:w-full [&>button]:w-full sm:[&>a]:w-auto sm:[&>button]:w-auto">
            {footer}
          </div>
        ) : null}
      </div>
    </div>
  );
}

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  desc,
  confirmText = "确认",
  danger,
  loading,
  children,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  desc?: ReactNode;
  confirmText?: string;
  danger?: boolean;
  loading?: boolean;
  children?: ReactNode;
}) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      width="max-w-md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} size="sm">
            取消
          </Button>
          <Button variant={danger ? "destructive" : "primary"} onClick={onConfirm} loading={loading} size="sm">
            {confirmText}
          </Button>
        </>
      }
    >
      <div className="text-[13px] leading-relaxed text-muted-foreground">{desc}</div>
      {children}
    </Dialog>
  );
}
