#!/bin/sh
# 用法：require-pass.sh <目录> <通过用例名的正则> <说明> -- <go test 参数...>
#
# 防"空转变绿"：在 <目录> 里 go test -v，除了退出码为 0，还必须真的有
# 一条名字匹配正则的 `--- PASS:`——"no tests to run"、全部 SKIP（缺
# TEST_PG_DSN 等环境）、-run 一个都没匹配上，一律算失败，不算通过。
dir=$1; pattern=$2; label=$3; shift 3
[ "$1" = "--" ] && shift
if [ ! -d "$dir" ]; then
	echo "FAIL $label：目录 $dir 不存在" >&2
	exit 1
fi
out=$(cd "$dir" && go test -v "$@" 2>&1)
status=$?
printf '%s\n' "$out"
if [ $status -ne 0 ]; then
	echo "FAIL $label：go test 退出码 $status" >&2
	exit 1
fi
if printf '%s\n' "$out" | grep -q 'no tests to run'; then
	echo "FAIL $label：no tests to run（-run 没匹配到任何用例，空转不算通过）" >&2
	exit 1
fi
if ! printf '%s\n' "$out" | grep -E -- '--- PASS: ' | grep -E -q -- "$pattern"; then
	echo "FAIL $label：没有任何名字匹配 /$pattern/ 的用例真正 PASS（全部 SKIP 或根本不存在，不算通过）" >&2
	exit 1
fi
echo "OK $label：至少一条匹配 /$pattern/ 的用例真实通过"
