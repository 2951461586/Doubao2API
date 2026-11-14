#!/bin/sh
# 打发布 tag：校验版本号 / 工作区 / 不重复，跑 make check，
# 然后把 CHANGELOG 里对应小节的正文作为 annotated tag 的说明。
#
# 用法：make release VERSION=v0.1.0
set -eu

ver="${1:-}"
case "$ver" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*)
	echo "用法: make release VERSION=v0.1.0" >&2
	exit 1
	;;
esac

plain="${ver#v}"

if [ -n "$(git status --porcelain)" ]; then
	echo "工作区不干净，请先提交（含 CHANGELOG 与版本号改动）" >&2
	exit 1
fi

if git rev-parse -q --verify "refs/tags/${ver}" >/dev/null; then
	echo "tag ${ver} 已存在" >&2
	exit 1
fi

if ! grep -q "^## \[${plain}\]" CHANGELOG.md; then
	echo "CHANGELOG.md 缺少 [${plain}] 小节，先补变更记录再发版" >&2
	exit 1
fi

echo "==> 发布前校验（fmt / vet / race）"
make check

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

{
	printf 'Doubao2API %s\n\n' "$ver"
	# 取该版本小节正文（到下一个 "## " 为止），去掉尾部空行。
	awk -v ver="$plain" '
		index($0, "## [" ver "]") { flag = 1; next }
		flag && /^## / { exit }
		flag { n++; line[n] = $0; if (NF) last = n }
		END { for (i = 1; i <= last; i++) print line[i] }
	' CHANGELOG.md
} >"$tmp"

git tag -a "$ver" -F "$tmp"
echo "==> 已创建 tag ${ver}"
echo
echo "推送："
echo "  git push origin HEAD"
echo "  git push origin ${ver}"
echo
git --no-pager show --stat --oneline "${ver}" | head -20
