#!/usr/bin/env python3
"""从 DoubaoWork 的 biz.pak 中提取全部 blob（逆向复刻用，可随时重跑）。

pak 格式（Lark/ByteDance 自定义包）：
  header:  version(u32 LE) | reserved(u32) | field2(u32)
  index :  N × { id(u16 LE) | offset(u32 LE) }，按 offset 升序
  data  :  第 i 个 blob = [offset_i, offset_{i+1})

格式随版本演进，index 的结束位置不能只信 header：
  * v<5 : field2 = 条目数（旧版布局）
  * v=5  : field2 不再是条目数（实测是校验/统计值），index 以
           { id=0, offset=文件长度 } 哨兵结束，随后还有一小段
           元数据（约 3 KB）才到首个 blob。
本脚本按「哨兵 / 首个 offset / field2」三重条件推断 index 长度，
新旧两种包都能拆。

每个 blob 若以 1f 8b 开头则 gzip 解压，再按内容特征推断扩展名。
注意：147 版本源包约 195 MB（旧版 224 MB），解压后约 525 MB（旧版 580 MB），
产物请勿提交到仓库。

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


def _read_index(f, version: int, field2: int, total: int) -> list:
    """读取 index，兼容旧版（field2=条目数）与 v5（哨兵结束）。"""
    f.seek(12)
    index = []
    first_offset = None
    while True:
        pos = f.tell()
        # 旧版：field2 是可信条目数时按数量截断
        if version < 5 and field2 and len(index) >= field2:
            break
        buf = f.read(6)
        if len(buf) < 6:
            break
        blob_id, offset = struct.unpack("<HI", buf)
        # 哨兵：{0, 文件长度}
        if blob_id == 0 and offset >= total:
            break
        # 索引不可能越过首个 blob 的起点
        if first_offset is not None and pos + 6 > first_offset:
            break
        if offset > total or offset <= pos:
            break
        index.append((blob_id, offset))
        if first_offset is None:
            first_offset = offset
    return index


def _extract_stream(f, pak_path: str, out_dir: str) -> int:
    header = f.read(12)
    version, _, field2 = struct.unpack("<III", header)
    total = os.path.getsize(pak_path)
    index = _read_index(f, version, field2, total)
    print(f"pak 版本={version} blob 数={len(index)}")
    index.sort(key=lambda r: r[1])

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
