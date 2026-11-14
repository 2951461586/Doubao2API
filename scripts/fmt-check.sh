#!/bin/sh
# 校验 gofmt：列出未格式化的文件并以非零退出（CI 与 make check 共用）。
set -eu

files=$(gofmt -l .)
if [ -n "$files" ]; then
	echo "以下文件未格式化（执行 make fmt）：" >&2
	echo "$files" >&2
	exit 1
fi
