import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, FolderOpen, HardDrive, CornerDownRight, Check } from "lucide-react";
import { api } from "../services/api";
import { Button } from "./ui/button";
import { Dialog } from "./ui/dialog";
import { ErrorBox, Loading } from "./ui/empty";
import { cn, formatNumber } from "../lib/utils";

/**
 * DirectoryPicker 是容器内目录浏览器。
 *
 * 后端 GET /api/roots/browse 只允许浏览已注册存储卷与白名单目录（MC_ALLOW_PATHS），
 * 所以这里的"快捷入口"直接来自后端返回的 roots 列表，避免用户面对一堆无权限的路径。
 */
export function DirectoryPicker({
  open,
  onClose,
  onPick,
  title = "选择目录",
  desc = "在容器内浏览并选择要扫描的目录",
  pickLabel = "选择当前目录",
}: {
  open: boolean;
  onClose: () => void;
  onPick: (path: string) => void;
  title?: string;
  desc?: string;
  pickLabel?: string;
}) {
  const [path, setPath] = useState<string | undefined>(undefined);

  // 每次重新打开都从默认根目录开始，避免停留在上次的深层路径
  useEffect(() => {
    if (open) setPath(undefined);
  }, [open]);

  const browse = useQuery({
    queryKey: ["browse", path ?? "__default__"],
    queryFn: () => api.browse(path),
    enabled: open,
    retry: false,
  });

  const data = browse.data;
  const roots = data?.roots ?? [];

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      desc={desc}
      width="max-w-xl"
      footer={
        <>
          <Button size="sm" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button
            size="sm"
            variant="primary"
            icon={<Check className="h-3.5 w-3.5" />}
            disabled={!data?.path}
            onClick={() => data && onPick(data.path)}
          >
            {pickLabel}
          </Button>
        </>
      }
    >
      <div className="space-y-2.5">
        {roots.length > 1 && (
          <div className="flex flex-wrap gap-1.5">
            {roots.map((rt) => (
              <button
                key={rt.path}
                onClick={() => setPath(rt.path)}
                title={rt.path}
                className={cn(
                  "flex items-center gap-1.5 rounded-md border px-2 py-1 text-[11.5px] transition-colors",
                  data?.path === rt.path || (data?.path ?? "").startsWith(rt.path)
                    ? "border-primary/40 bg-primary/[0.06] text-primary"
                    : "border-border text-muted-foreground hover:bg-muted",
                )}
              >
                <HardDrive className="h-3 w-3" />
                <span className="max-w-[130px] truncate">{rt.name}</span>
              </button>
            ))}
          </div>
        )}

        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" disabled={!data?.parent} onClick={() => setPath(data?.parent)}>
            <ChevronLeft className="h-3.5 w-3.5" /> 上级
          </Button>
          <div className="min-w-0 flex-1 truncate rounded border border-border bg-muted/50 px-2 py-1 font-mono text-[11px]">
            {data?.path ?? "…"}
          </div>
        </div>

        {browse.isLoading ? (
          <Loading label="读取目录…" className="py-6" />
        ) : browse.error ? (
          <ErrorBox message={(browse.error as Error).message} />
        ) : (
          <div className="max-h-[320px] overflow-y-auto rounded border border-border">
            {data?.dirs.length === 0 ? (
              <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
                没有子目录，可以直接选择当前目录
              </div>
            ) : (
              data?.dirs.map((d) => (
                <button
                  key={d.path}
                  onClick={() => setPath(d.path)}
                  className="flex w-full items-center gap-2 border-b border-border/60 px-2.5 py-1.5 text-left text-[12px] last:border-0 hover:bg-muted/60"
                >
                  <FolderOpen className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{d.name}</span>
                  <CornerDownRight className="ml-auto h-3 w-3 shrink-0 text-muted-foreground/60" />
                </button>
              ))
            )}
          </div>
        )}

        {data ? (
          <div className="flex items-center justify-between text-[11px] text-muted-foreground">
            <span>该目录下 {formatNumber(data.file_count)} 个文件</span>
            <span className="font-mono">{data.path}</span>
          </div>
        ) : null}
      </div>
    </Dialog>
  );
}
