#!/bin/sh
# 覆盖率门槛：防止重构把测试覆盖悄悄吃掉。
#
#   ./scripts/coverage-gate.sh coverage.out
#
# 判定方式：按包对所有函数的覆盖率取平均（与 `go tool cover -func` 的
# 最后一行 total 不同——total 是语句加权，会被大文件主导）。
#
# 阈值在 v0.2 重构后重新校准：server / doubao 是主要业务面，store 是持久化面。
set -eu

out="${1:-coverage.out}"
if [ ! -f "$out" ]; then
	echo "缺少覆盖率文件: $out（先运行 go test -coverprofile=$out ./...）" >&2
	exit 1
fi

go tool cover -func="$out" | awk '
{
	split($1, parts, ":")
	path = parts[1]
	pct = $3
	gsub(/%/, "", pct)
	if (path ~ /\/server\//) { s += pct; sn++ }
	if (path ~ /\/doubao\//) { d += pct; dn++ }
	if (path ~ /\/store\//)  { t += pct; tn++ }
	if (path ~ /\/account\//){ a += pct; an++ }
}
END {
	fail = 0
	if (sn) {
		s /= sn
		printf "server   %.1f%% (>= 55)\n", s
		if (s < 55) { printf "::error::server 覆盖率 %.1f%% < 55%%\n", s; fail = 1 }
	}
	if (dn) {
		d /= dn
		printf "doubao   %.1f%% (>= 32)\n", d
		if (d < 32) { printf "::error::doubao 覆盖率 %.1f%% < 32%%\n", d; fail = 1 }
	}
	if (tn) {
		t /= tn
		printf "store    %.1f%% (>= 90)\n", t
		if (t < 90) { printf "::error::store 覆盖率 %.1f%% < 90%%\n", t; fail = 1 }
	}
	if (an) {
		a /= an
		printf "account  %.1f%% (>= 90)\n", a
		if (a < 90) { printf "::error::account 覆盖率 %.1f%% < 90%%\n", a; fail = 1 }
	}
	exit fail
}'
