#!/usr/bin/env python3
"""从 biz.pak 解包产物中还原 source map 里的原始源码（逆向复刻用，可随时重跑）。

`extract_pak.py` 只负责把 pak 拆成一堆 blob；真正的源码藏在 webpack source map
（JSON，含 `sources` / `sourcesContent`）里。本脚本把这些源码按原目录结构落盘。

路径映射规则（对齐 webpack 的 `webpack://` 伪协议）：

    webpack://<bundle>/../../L1-Arch/foo.ts          -> _/_/L1-Arch/foo.ts
    webpack://<bundle>/../../../node_modules/x/y.js  -> _/_/_/nm/x/y.js

即：`..` 段 -> `_`，`node_modules` -> `nm`。

用法:
    python tools/extract_sources.py .recon/extract .recon/src
    python tools/extract_sources.py .recon/extract .recon/src --stats
"""
import json
import os
import re
import sys

# webpack://<bundle>/ 前缀
PREFIX_RE = re.compile(r"^webpack://[^/]*/")


def map_source_path(src: str) -> str | None:
    """把 source map 里的 source 路径映射为仓库内的相对路径。"""
    path = PREFIX_RE.sub("", src)
    # 去掉 query / 前导 ./
    path = path.split("?", 1)[0]
    segs = []
    for seg in path.split("/"):
        if seg in ("", "."):
            continue
        if seg == "..":
            segs.append("_")
        elif seg == "node_modules":
            segs.append("nm")
        else:
            segs.append(seg)
    if not segs:
        return None
    rel = "/".join(segs)
    # 安全兜底：不允许绝对路径或越权
    if rel.startswith("/") or ":" in rel.split("/")[0]:
        return None
    return rel


def _long_path(path: str) -> str:
    """Windows 下用 \\\\?\\ 前缀绕开 MAX_PATH 限制（.pnpm 路径很容易超 260 字符）。"""
    if os.name != "nt":
        return path
    p = os.path.abspath(path)
    if p.startswith("\\\\?\\"):
        return p
    return "\\\\?\\" + p


def extract(extract_dir: str, out_dir: str, stats: bool = False) -> int:
    written = 0
    maps = 0
    skipped = 0
    seen = set()

    try:
        names = sorted(os.listdir(extract_dir))
    except OSError as e:
        raise SystemExit(f"无法读取解包目录 {extract_dir}: {e}") from e

    for name in names:
        if not name.endswith(".json"):
            continue
        full = os.path.join(extract_dir, name)
        try:
            with open(full, encoding="utf-8", errors="replace") as f:
                data = json.load(f)
        except (OSError, ValueError):
            continue
        if not isinstance(data, dict):
            continue
        sources = data.get("sources")
        contents = data.get("sourcesContent")
        if not isinstance(sources, list) or not isinstance(contents, list):
            continue
        maps += 1
        for src, content in zip(sources, contents, strict=False):
            if not isinstance(src, str) or not isinstance(content, str):
                skipped += 1
                continue
            rel = map_source_path(src)
            if rel is None or rel in seen:
                skipped += 1
                continue
            seen.add(rel)
            dest = _long_path(os.path.join(out_dir, rel))
            try:
                os.makedirs(os.path.dirname(dest), exist_ok=True)
                with open(dest, "w", encoding="utf-8", newline="\n") as out:
                    out.write(content)
            except OSError as e:
                print(f"跳过 {rel}: {e}", file=sys.stderr)
                skipped += 1
                continue
            written += 1

    if stats:
        print(f"source map 数: {maps}")
        print(f"还原文件数  : {written}")
        print(f"跳过        : {skipped}")
    return written


if __name__ == "__main__":
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    if len(args) != 2:
        print(__doc__)
        sys.exit(1)
    n = extract(args[0], args[1], stats="--stats" in sys.argv)
    print(f"restored {n} sources -> {args[1]}")
