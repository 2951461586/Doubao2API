"""在客户端 bundle 中按关键字提取上下文（minified 单行 JS 的可读切片）。

用法: python scripts/recon_ctx.py <文件> <关键字> [--window N] [--limit M]
"""
import argparse
import pathlib
import sys


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("file")
    ap.add_argument("keyword")
    ap.add_argument("--window", type=int, default=260, help="命中点左右各取多少字符")
    ap.add_argument("--limit", type=int, default=6, help="最多打印多少处")
    args = ap.parse_args()

    try:
        data = pathlib.Path(args.file).read_bytes()
    except OSError as exc:
        sys.exit(f"无法读取: {exc}")
    text = data.decode("utf-8", "replace")
    key = args.keyword

    pos, shown, total = 0, 0, 0
    while True:
        pos = text.find(key, pos)
        if pos < 0:
            break
        total += 1
        if shown < args.limit:
            lo = max(0, pos - args.window)
            hi = min(len(text), pos + len(key) + args.window)
            snippet = text[lo:hi].replace("\n", " ")
            # 在命中处加标记
            mark = pos - lo
            print(f"--- #{shown + 1} @{pos}")
            print("  ..." + snippet[:mark] + "【" + key + "】" + snippet[mark + len(key):] + "...")
            shown += 1
        pos += len(key)

    print(f"\n共 {total} 处命中，展示 {shown} 处")


if __name__ == "__main__":
    main()
