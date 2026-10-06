#!/usr/bin/env python3
"""从 DoubaoWork 的 biz.pak 中提取全部 blob（逆向复刻用，可随时重跑）。

pak 格式（Lark/ByteDance 自定义包）：
  header:  version(u32 LE) | reserved(u32) | count(u32)
  index :  count × { id(u16 LE) | offset(u32 LE) }，按 offset 升序
  data  :  第 i 个 blob = [offset_i, offset_{i+1})

每个 blob 若以 1f 8b 开头则 gzip 解压，再按内容特征推断扩展名。
注意：源包约 224 MB，解压后约 580 MB，产物请勿提交到仓库。

用法:
    python extract_pak.py <biz.pak> <输出目录>
"""
import gzip
import os
import struct
import sys


def extract(pak_path: str, out_dir: str) -> int:
    try:
        os.makedirs(out_dir, exist_ok=True)
    except OSError as e:
        raise SystemExit(f"无法创建输出目录 {out_dir}: {e}") from e

    try:
        with open(pak_path, "rb") as f:
            return _extract_stream(f, pak_path, out_dir)
    except OSError as e:
        raise SystemExit(f"读取 {pak_path} 失败: {e}") from e


def _extract_stream(f, pak_path: str, out_dir: str) -> int:
    header = f.read(12)
    version, _, count = struct.unpack("<III", header)
    print(f"pak 版本={version} blob 数={count}")
    f.seek(12)
    index = [struct.unpack("<HI", f.read(6)) for _ in range(count)]
    index.sort(key=lambda r: r[1])
    total = os.path.getsize(pak_path)

    written = 0
    for i, (blob_id, offset) in enumerate(index):
        end = index[i + 1][1] if i + 1 < len(index) else total
        if offset >= total or end <= offset:
            continue
        f.seek(offset)
        blob = f.read(end - offset)
        if len(blob) < 3:
            continue
        if blob[:2] == b"\x1f\x8b":
            try:
                blob = gzip.decompress(blob)
            except OSError:
                continue
        dest = os.path.join(out_dir, f"{blob_id:05d}.{guess_ext(blob)}")
        try:
            with open(dest, "wb") as out:
                out.write(blob)
        except OSError as e:
            print(f"跳过 blob {blob_id}: {e}", file=sys.stderr)
            continue
        written += 1
    return written


def guess_ext(b: bytes) -> str:
    if b[:4] == b"\x89PNG":
        return "png"
    if b[:2] == b"\xff\xd8":
        return "jpg"
    if b[:4] in (b"wOF2",):
        return "woff2"
    if b[:4] in (b"wOFF",):
        return "woff"
    if b[:4] == b"\x00asm":
        return "wasm"
    head = b[:200]
    stripped = head.lstrip()
    if stripped[:1] in (b"{", b"["):
        return "json"
    if any(k in head for k in (b"function", b"!function", b"(()=>", b"export ", b"const ", b"var ")):
        return "js"
    if b"{" in head and b":" in head and b'"' in head:
        return "json"
    return "bin"


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(__doc__)
        sys.exit(1)
    n = extract(sys.argv[1], sys.argv[2])
    print(f"extracted {n} blobs -> {sys.argv[2]}")
