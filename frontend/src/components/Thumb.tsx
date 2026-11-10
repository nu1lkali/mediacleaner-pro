import { useEffect, useState } from "react";
import { FileAudio, FileText, Film, ImageOff, Images, Loader2, RefreshCw } from "lucide-react";
import { api } from "../services/api";
import type { ProxyState } from "../services/types";
import { Button } from "./ui/button";
import { cn } from "@/lib/utils";

/**
 * 缩略图。图片走 /thumb，视频优先 /thumb（后端抽第一帧），失败回落到类型图标。
 * 加载失败时不再反复重试（避免列表里刷满 500 请求）。
 * 传 quarantineId 时走隔离区接口（文件已不在库里，files 表的 id 链路不可用）。
 */
export function Thumb({
  fileId,
  quarantineId,
  kind,
  className,
  width = 120,
}: {
  fileId?: number;
  quarantineId?: number;
  kind: string;
  className?: string;
  width?: number;
}) {
  const [failed, setFailed] = useState(false);
  const isVisual = kind === "image" || kind === "video";

  if (!isVisual || failed) {
    return (
      <div className={cn("flex items-center justify-center bg-muted text-muted-foreground", className)}>
        {kind === "audio" ? (
          <FileAudio className="h-3.5 w-3.5" />
        ) : kind === "video" ? (
          <Film className="h-3.5 w-3.5" />
        ) : kind === "image" ? (
          <ImageOff className="h-3.5 w-3.5" />
        ) : (
          <FileText className="h-3.5 w-3.5" />
        )}
      </div>
    );
  }

  return (
    <img
      src={quarantineId != null ? api.quarantineThumbUrl(quarantineId, width) : api.thumbUrl(fileId ?? 0, width)}
      alt=""
      loading="lazy"
      decoding="async"
      onError={() => setFailed(true)}
      className={cn("object-cover", className)}
    />
  );
}

/**
 * 大图预览。
 * 视频默认走 /raw 直链（后端 http.ServeContent 已支持 Range），零转码零等待；
 * 浏览器解不了时才给出明确提示，并把「生成转码预览」作为手动兜底
 * （隔离区文件没有转码链路，兜底只提供下载）。
 */
export function Preview({
  fileId,
  quarantineId,
  kind,
  className,
}: {
  fileId?: number;
  quarantineId?: number;
  kind: string;
  className?: string;
}) {
  const [imageFailed, setImageFailed] = useState(false);

  if (kind === "video") {
    return <VideoPreview fileId={fileId} quarantineId={quarantineId} className={className} />;
  }
  if (kind === "image" && !imageFailed) {
    return (
      <img
        src={quarantineId != null ? api.quarantineFileUrl(quarantineId) : api.rawUrl(fileId ?? 0)}
        alt=""
        onError={() => setImageFailed(true)}
        className={className}
      />
    );
  }
  return (
    <div className={cn("flex items-center justify-center text-[12px] text-muted-foreground", className)}>
      <div className="flex flex-col items-center gap-1.5">
        <Images className="h-5 w-5" />
        该类型不支持预览
      </div>
    </div>
  );
}

type PreviewMode = "direct" | "fallback" | "converting" | "proxy";

function VideoPreview({
  fileId,
  quarantineId,
  className,
}: {
  fileId?: number;
  quarantineId?: number;
  className?: string;
}) {
  const [mode, setMode] = useState<PreviewMode>("direct");
  const [ready, setReady] = useState(false);
  const [status, setStatus] = useState<ProxyState | null>(null);
  const [notice, setNotice] = useState("");
  const [attempt, setAttempt] = useState(0);
  // 隔离区文件不走转码代理（那条链路按 files 表工作），只有直链 + 下载兜底
  const isQuarantined = quarantineId != null;
  const directSrc = isQuarantined ? api.quarantineFileUrl(quarantineId) : api.rawUrl(fileId ?? 0);

  // 兜底看门狗：某些容器/编码浏览器既不报错也不加载，直接把 <video> 挂成无限转圈。
  // 10 秒还没读到元数据就切到说明卡片，保证任何时候都有明确反馈。
  useEffect(() => {
    if ((mode !== "direct" && mode !== "proxy") || ready) return;
    const timer = window.setTimeout(() => {
      setNotice("浏览器在 10 秒内没能解码该视频，通常是容器或编码不受支持。");
      setMode("fallback");
    }, 10000);
    return () => window.clearTimeout(timer);
  }, [mode, ready, attempt]);

  // 转码状态轮询：probe 接口非阻塞，idle 时由后端启动后台转码。
  useEffect(() => {
    if (mode !== "converting") return;
    let alive = true;
    const poll = async () => {
      try {
        const st = await api.proxyStatus(fileId ?? 0);
        if (!alive) return;
        setStatus(st);
        if (st.status === "ready") {
          setReady(false);
          setMode("proxy");
        } else if (st.status === "failed" || st.status === "unavailable") {
          setNotice(st.reason || "转码预览不可用");
          setMode("fallback");
        }
      } catch (e) {
        if (alive) setNotice((e as Error).message);
      }
    };
    void poll();
    const timer = window.setInterval(poll, 2000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [mode, fileId, attempt]);

  const startTranscode = () => {
    if (isQuarantined) return;
    setNotice("");
    setStatus(null);
    setReady(false);
    setAttempt((a) => a + 1);
    setMode("converting");
  };

  if (mode === "direct" || mode === "proxy") {
    return (
      <video
        key={`${mode}-${attempt}`}
        src={mode === "direct" ? directSrc : api.proxyUrl(fileId ?? 0)}
        poster={isQuarantined ? api.quarantineThumbUrl(quarantineId, 640) : api.thumbUrl(fileId ?? 0, 640)}
        controls
        playsInline
        preload="metadata"
        onLoadedMetadata={() => setReady(true)}
        onError={() => {
          setNotice("浏览器无法直接解码该文件（常见于 MKV/AVI/TS 容器，或 HEVC/H.265 视频、DTS/AC3 音轨）。");
          setMode("fallback");
        }}
        className={className}
      />
    );
  }

  if (mode === "converting") {
    const pct = status?.percent ?? 0;
    return (
      <div className={cn("flex flex-col items-center justify-center gap-3 bg-muted/40 p-6 text-center", className)}>
        <Loader2 className="h-6 w-6 shrink-0 animate-spin text-primary" />
        <div className="text-[13px] font-medium text-foreground">
          {status?.message || "正在生成可播放预览…"}
        </div>
        <div className="w-full max-w-xs">
          <div className="h-1.5 overflow-hidden rounded-full bg-border">
            <div
              className="h-full rounded-full bg-primary transition-all duration-500"
              style={{ width: `${Math.max(pct, 3)}%` }}
            />
          </div>
          <div className="mt-1.5 text-[11px] text-muted-foreground">
            {pct > 0 ? `已完成 ${pct}%` : "首次转码较慢，完成后会自动开始播放并缓存结果"}
          </div>
        </div>
        <Button size="sm" variant="ghost" onClick={() => setMode("fallback")}>
          取消
        </Button>
      </div>
    );
  }

  return (
    <div className={cn("flex flex-col items-center justify-center gap-3 bg-muted/40 p-5 text-center", className)}>
      <Film className="h-7 w-7 shrink-0 text-muted-foreground" />
      <div className="text-[13px] font-medium text-foreground">当前浏览器不能直接播放该格式</div>
      <p className="max-w-md text-[11.5px] leading-relaxed text-muted-foreground">
        {notice ||
          "常见于 MKV/AVI/TS 容器，或 HEVC/H.265 视频、DTS/AC3 音轨。文件内容本身完好，可用本地播放器打开。"}
        {isQuarantined ? " 隔离区文件不提供转码预览，可下载后用本地播放器打开。" : ""}
      </p>
      <div className="flex flex-wrap items-center justify-center gap-2">
        {!isQuarantined && (
          <Button
            size="sm"
            variant="primary"
            icon={<RefreshCw className="h-3.5 w-3.5" />}
            onClick={startTranscode}
          >
            生成转码预览
          </Button>
        )}
        <a href={directSrc} target="_blank" rel="noreferrer" download>
          <Button size="sm" variant="outline">
            下载原文件
          </Button>
        </a>
      </div>
      {status?.reason ? (
        <div className="max-w-md break-words text-[11px] text-destructive">{status.reason}</div>
      ) : null}
    </div>
  );
}
