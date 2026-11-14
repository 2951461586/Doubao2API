"""在 biz.pak 内按关键字定位 blob，只导出命中的 blob 供进一步分析。

用法: python scripts/recon_pak_probe.py "<pak 路径>" "<输出目录>" [关键字...]
"""
import mmap
import pathlib
import struct
import sys

DEFAULT_KEYS = [
    b"Agw-Js-Conv",
    b"x-tt-passport-csrf-token",
    b"model_item_key",
    b"need_deep_think",
    b"action_bar_skill_id",
    b"/samantha/",
    b"tool_call",
    b"function_call",
    b"reasoning_effort",
    b"pc_incentive_task_mvp",
]


def read_index(buf):
    version, reserved, field2 = struct.unpack_from("<III", buf, 0)
    entries = []
    off = 12
    prev = 0
    while off + 6 <= len(buf):
        eid, eoff = struct.unpack_from("<HI", buf, off)
        if eoff <= prev or eoff > len(buf):
            break
        entries.append((eid, eoff))
        prev = eoff
        off += 6
        if eid == 0:  # v5 哨兵
            break
    return version, entries, off


def main():
    pak = pathlib.Path(sys.argv[1])
    outdir = pathlib.Path(sys.argv[2])
    keys = [k.encode() if isinstance(k, str) else k for k in (sys.argv[3:] or DEFAULT_KEYS)]
    outdir.mkdir(parents=True, exist_ok=True)

    try:
        f = open(pak, "rb")
    except OSError as exc:
        sys.exit(f"无法读取 pak: {exc}")

    with f:
        buf = mmap.mmap(f.fileno(), 0, access=mmap.ACCESS_READ)
        version, entries, index_end = read_index(buf)
        print(f"pak: version={version} blobs={len(entries)} index_end={index_end} size={len(buf)}")

        # 计算 blob 边界：下一个 blob 的 offset（或文件尾）
        bounds = []
        for i, (eid, start) in enumerate(entries):
            end = entries[i + 1][1] if i + 1 < len(entries) else len(buf)
            bounds.append((eid, start, end))

        hits = {}
        for key in keys:
            pos = 0
            while True:
                pos = buf.find(key, pos)
                if pos < 0:
                    break
                blob = None
                for eid, start, end in bounds:
                    if start <= pos < end:
                        blob = (eid, start, end)
                        break
                if blob:
                    hits.setdefault(blob[0], set()).add(key.decode())
                pos += 1

        print(f"命中 blob 数: {len(hits)}")
        for eid, ks in sorted(hits.items()):
            start, end = next((s, e) for i, s, e in bounds if i == eid)
            size = end - start
            print(f"  blob id={eid:<6} size={size:<9} keys={sorted(ks)}")
            path = outdir / f"blob-{eid}.bin"
            path.write_bytes(buf[start:end])
        buf.close()


if __name__ == "__main__":
    main()
