#!/usr/bin/env python
"""生成测试媒体库，覆盖去重验收场景。

场景：
  v1/photos/cat.png 与 v2/photos/cat.png   —— 完全相同的图片（跨卷重复）
  v1/photos/cat_small.png                  —— 同图缩放到一半（相似图片）
  v1/video/movie.mp4 与 v2/video/backup/movie.mp4 —— 完全相同的视频文件
  v1/video/movie.nfo / movie.srt           —— 附属文件（验证关联清理保护）
  v1/docs/report.pdf 与 v1/docs/report-copy.pdf   —— 同目录内容重复、文件名不同
  v1/docs/readme.md                        —— 唯一文件（不应被误判）
"""
import os
import struct
import zlib
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
BASE = ROOT / "testdata"


def pixels(w: int, h: int, seed: int) -> list[bytearray]:
    """生成有足够纹理的像素行（纯渐变会让 pHash 过于相似，故叠加块状图案）。

    返回每行 RGB 字节串，便于下游做真正的降采样。
    """
    rows: list[bytearray] = []
    for y in range(h):
        row = bytearray()
        for x in range(w):
            block = ((x // 8) + (y // 8)) % 2
            r = (x * 255 // max(1, w - 1)) if block else 255 - (x * 255 // max(1, w - 1))
            g = (y * 255 // max(1, h - 1)) if block else 255 - (y * 255 // max(1, h - 1))
            b = (r + g + seed * 37) % 256
            row += bytes((r, g, b))
        rows.append(row)
    return rows


def downscale(rows: list[bytearray], factor: int) -> list[bytearray]:
    """按整数倍做最近邻降采样 —— 保证与源图内容一致，只损失分辨率。"""
    out: list[bytearray] = []
    for y in range(0, len(rows), factor):
        src = rows[y]
        row = bytearray()
        for x in range(0, len(src) // 3, factor):
            row += src[x * 3 : x * 3 + 3]
        out.append(row)
    return out


def encode_png(w: int, h: int, rows: list[bytearray]) -> bytes:
    raw = b"".join(bytes(b"\x00") + bytes(r) for r in rows)

    def chunk(tag: bytes, data: bytes) -> bytes:
        return (struct.pack(">I", len(data)) + tag + data
                + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF))

    ihdr = struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)
    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", ihdr)
            + chunk(b"IDAT", zlib.compress(raw, 9))
            + chunk(b"IEND", b""))


def png_bytes(w: int, h: int, seed: int) -> bytes:
    return encode_png(w, h, pixels(w, h, seed))


def fake_mp4(filler: int) -> bytes:
    """构造一个带 ftyp + mdat 的最小 MP4 结构（足够被识别为视频文件）。"""
    ftyp = b"isom" + struct.pack(">I", 512) + b"isomiso2avc1mp41"
    ftyp_box = struct.pack(">I", 8 + len(ftyp)) + b"ftyp" + ftyp
    payload = bytes((i * 31 + filler) % 256 for i in range(4096))
    mdat_box = struct.pack(">I", 8 + len(payload)) + b"mdat" + payload
    return ftyp_box + mdat_box


def write(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)
    print(f"  {path.relative_to(ROOT)}  {len(data)} B")


def main() -> None:
    print(f"生成测试数据到 {BASE}")
    big_rows = pixels(640, 480, seed=3)
    big = encode_png(640, 480, big_rows)
    # 真·降采样：内容与 big 一致，只是分辨率减半 —— 用来验证相似图片检测
    small = encode_png(320, 240, downscale(big_rows, 2))

    write(BASE / "v1" / "photos" / "cat.png", big)
    write(BASE / "v2" / "photos" / "cat.png", big)          # 跨卷完全重复
    write(BASE / "v1" / "photos" / "cat_small.png", small)  # 相似（缩放版）
    write(BASE / "v1" / "photos" / "cat.nfo", b"<movie><title>Cat</title></movie>\n")
    write(BASE / "v1" / "photos" / "cat.srt", b"1\n00:00:01,000 --> 00:00:02,000\nMeow\n")

    movie = fake_mp4(7)
    write(BASE / "v1" / "video" / "movie.mp4", movie)
    write(BASE / "v2" / "video" / "backup" / "movie.mp4", movie)  # 跨卷完全重复
    write(BASE / "v1" / "video" / "movie.nfo", b"<movie><title>Demo</title></movie>\n")

    report = b"%PDF-1.4\n" + bytes(range(256)) * 40
    write(BASE / "v1" / "docs" / "report.pdf", report)
    write(BASE / "v1" / "docs" / "report-copy.pdf", report)  # 同目录不同名，内容相同

    write(BASE / "v1" / "docs" / "readme.md", b"# Media library\nnot a duplicate\n")
    print("完成")


if __name__ == "__main__":
    main()
