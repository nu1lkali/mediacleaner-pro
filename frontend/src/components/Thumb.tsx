import { useState } from "react";
import { FileAudio, FileText, Film, ImageOff, Images } from "lucide-react";
import { api } from "../services/api";
import { cn } from "@/lib/utils";

/**
 * 缩略图。图片走 /thumb，视频优先 /thumb（后端抽第一帧），失败回落到类型图标。
 * 加载失败时不再反复重试（避免列表里刷满 500 请求）。
 */
export function Thumb({
  fileId,
  kind,
  className,
  width = 120,
}: {
  fileId: number;
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
      src={api.thumbUrl(fileId, width)}
      alt=""
      loading="lazy"
      decoding="async"
      onError={() => setFailed(true)}
      className={cn("object-cover", className)}
    />
  );
}

/** 大图预览：图片用原图，视频用转码代理流。 */
export function Preview({ fileId, kind, className }: { fileId: number; kind: string; className?: string }) {
  const [failed, setFailed] = useState(false);

  if (kind === "video" && !failed) {
    return (
      <video
        src={api.proxyUrl(fileId)}
        poster={api.thumbUrl(fileId, 640)}
        controls
        onError={() => setFailed(true)}
        className={className}
      />
    );
  }
  if (kind === "image" && !failed) {
    return (
      <img src={api.rawUrl(fileId)} alt="" onError={() => setFailed(true)} className={className} />
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
