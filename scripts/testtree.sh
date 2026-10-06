#!/bin/sh
# 开发树 → 测试树的单向同步与一致性校验。
#
# 为什么要两个树：重构现场（开发树）只放源码。编译产物、跑起来的进程、它写出来的
# config.yaml / data/ / log/ 都留在测试树里，这样"顺手 build 一下"或"起服务点验一下"
# 不会在开发树里留下任何可能被人当源码提交的东西。
#
# 为什么校验用内容而不是 git：调试期间的修改要求两树**一起**改，而此时开发树的改动往往
# 还没提交。所以两边都比"tracked + 未忽略的 untracked"这份清单的逐文件摘要 —— 清单或摘要
# 任何一处不一致就报出来，测试树自己的 config.yaml/data/ 因为是 ignored 项不参与、也不被删。
#
# 用法（都在开发树里执行）：
#   scripts/testtree.sh sync      # 把开发树的源码灌进测试树（删除测试树里多出来的源码文件）
#   scripts/testtree.sh verify    # 两树是否同一份源码；不一致就非零退出并列出差异
#   scripts/testtree.sh gates     # sync + verify，然后在测试树里跑全套门禁并 build
#   scripts/testtree.sh run       # sync + verify，然后在测试树里 build 并起服务
#
# 测试树位置：$CSAI_TESTTREE，默认 $HOME/csai-测试版
set -eu

DEV=$(git rev-parse --show-toplevel)
TEST=${CSAI_TESTTREE:-$HOME/csai-测试版}

die() { echo "testtree: $*" >&2; exit 1; }

[ "$TEST" != "$DEV" ] || die "测试树不能指向开发树自己（$DEV）"
[ -d "$TEST" ] || die "测试树不存在：$TEST。建法：git clone --single-branch --branch main \"$DEV\" \"$TEST\"，再在里面放一份自己的 config.yaml"
[ -f "$DEV/go.mod" ] || die "必须在 csai 开发树里执行"

# 代码路径：这些目录/文件里两树必须一模一样；能力内容目录（bundles/ skills/ agents/
# tools/ knowledge_base/ images/）允许测试树有自己的东西，因为它就是点验的输入。
is_code() {
	case "$1" in
	internal/* | cmd/* | web/* | scripts/* | docs/* | .github/*) return 0 ;;
	Makefile | go.mod | go.sum | .gitignore | .golangci.yml | .go-arch-lint.yml | config.example.yaml) return 0 ;;
	*) return 1 ;;
	esac
}

# 一次树扫描出"应当一致的清单"：受版本文件 + 未被 .gitignore 忽略的新文件。
manifest() {
	(
		cd "$1"
		{
			# -c core.quotePath=false is not cosmetic. This repo ships Chinese file names
			# (roles/AI应用红队测试.yaml, bundles/*/roles/*.yaml, docs/...), and git's default is to
			# print them octal-escaped and quoted: `"roles/API\345\256\211...yaml"`. That string is
			# not a path, so `[ -f ]` failed, tar skipped the file, and verify reported a clean
			# comparison while never having looked at any of those ~40 files - including the ones
			# the gates count. Raw names make the manifest what it claims to be.
			git -c core.quotePath=false ls-files
			git -c core.quotePath=false ls-files --others --exclude-standard
		} | sort -u | while IFS= read -r entry; do
			# A file deleted in the working tree but not staged is still in `ls-files`, and keeping it
			# in the manifest made sync *print* "sync 完成" while the stale copy stayed in the test
			# tree - the delete list never saw it go away. Existence (or being a symlink) decides.
			if [ -e "$entry" ] || [ -L "$entry" ]; then
				printf '%s\n' "$entry"
			fi
		done
	)
}

# 清单里的路径必须是**能用的路径**。git 一旦恢复 quotePath（改名、升级、别人的 alias），非 ASCII
# 文件名就变成 `"roles/\345\256...yaml"` 这种转义串：行号对得上、名字对不上，于是 tar 不复制、
# shasum 读不到，而 verify 照样报"源码一致"——它比对的文件比它以为的少了几十个。所以清单只要出现
# 引号包裹的条目就当场停，而不是等到门禁在缺文件的情况下假装通过。
assert_quoted_free() {
	quoted=$(grep '"' "$1" || true)
	if [ -n "$quoted" ]; then
		echo "testtree: 清单里有被 git 转义引用的路径，比对会漏文件（修法：core.quotePath=false）：" >&2
		printf '%s\n' "$quoted" | sed 's/^/  /' >&2
		exit 1
	fi
}

# 清单 + 每个文件的摘要。文件在本树里不存在时 shasum 只告警，那行因此缺失，
# 与另一树的差异会被 compare 出来 —— 缺失本身也是不一致，不会被静默放过。
digests() {
	(
		cd "$1"
		manifest "$1" | tr '\n' '\0' | xargs -0 shasum 2>/dev/null | sort -k 2
	)
}

compare() {
	d1=$(mktemp)
	d2=$(mktemp)
	digests "$DEV" >"$d1"
	digests "$TEST" >"$d2"
	n=$(wc -l <"$d1" | tr -d ' ')
	changed=$(mktemp)
	if cmp -s "$d1" "$d2"; then
		: >"$changed"
	else
		diff "$d1" "$d2" | sed -n 's/^[<>] [0-9a-f]\{40\}  //p' | sort -u >"$changed"
	fi
	# 分级：代码路径的差异是硬失败；能力内容差异只提示（测试树放了自己的包是正当的）
	code_drift=""
	for f in $(cat "$changed"); do
		if is_code "$f"; then
			code_drift="$code_drift $f"
		else
			echo "提示：能力内容与另一树不同（不算失败，测试树可以有自己的包）：$f" >&2
		fi
	done
	rm -f "$d1" "$d2" "$changed"
	if [ -z "$code_drift" ]; then
		echo "verify ok: 开发树与测试树源码一致（$(cd "$DEV" && git rev-parse --short HEAD)，$n 个文件已比对）"
		return 0
	fi
	echo "verify FAILED: 以下代码文件两树不一致：" >&2
	for f in $code_drift; do echo "  $f" >&2; done
	echo "修法：在开发树里改，然后 scripts/testtree.sh sync —— 不要在测试树里单独编辑源码。" >&2
	return 1
}

sync() {
	only_in_test=$(mktemp)
	dev_list=$(mktemp)
	test_list=$(mktemp)
	manifest "$DEV" >"$dev_list"
	manifest "$TEST" >"$test_list"
	assert_quoted_free "$dev_list"
	assert_quoted_free "$test_list"

	# 先灌：开发树清单里的每个、且确实还在开发树上的文件（tracked 但已被删掉的文件仍在
	# git ls-files 里，它们由下面的"后删"处理）。ignored 的运行时文件天然不在清单里，
	# 所以测试树的 config.yaml / data/ / log/ 不会被这份 tar 覆盖。
	present=$(mktemp)
	while IFS= read -r f; do
		[ -f "$DEV/$f" ] && printf '%s\n' "$f" >>"$present"
	done <"$dev_list"
	(cd "$DEV" && tar -cf - -T "$present") | (cd "$TEST" && tar -xf -)
	rm -f "$present"

	# 后删：只删**代码路径**下、开发树已经没有了的文件。这个限定不是洁癖：测试树里手工放的
	# 一个能力包（bundles/<mytest>/）、一个测试用 skill 目录，同样是"untracked 且未被忽略"
	# 因而进清单，删掉它们等于把测试现场本身清空了。非代码路径的差异只在 verify 里提示，不删。
	comm -13 "$dev_list" "$test_list" >"$only_in_test"
	keep=$(mktemp)
	while IFS= read -r f; do
		if is_code "$f"; then
			echo "  删除（开发树已没有该源码文件）：$f" >&2
			rm -f "$TEST/$f"
		else
			printf '%s\n' "$f" >>"$keep"
		fi
	done <"$only_in_test"
	if [ -s "$keep" ]; then
		echo "测试树独有的非源码文件，保留不动（要清掉请自己确认后再删）："
		sed 's/^/  /' "$keep"
	fi
	rm -f "$only_in_test" "$dev_list" "$test_list" "$keep"
	echo "sync 完成：$(cd "$DEV" && git rev-parse --short HEAD) 的开发树源码已进 $TEST"
}

case "${1:-verify}" in
sync)
	sync
	compare
	;;
verify)
	compare
	;;
gates)
	sync
	compare
	(cd "$TEST" && make -f Makefile fmt-check vet layering-check wiring-check js-check test-race)
	(cd "$TEST" && make -f Makefile build build-stdio)
	echo "gates ok：门禁与构建都在测试树里跑完，开发树没有被编译产物碰过"
	;;
run)
	sync
	compare
	(cd "$TEST" && make -f Makefile build)
	echo "在测试树启动：$TEST（端口与数据库都是它自己的，Ctrl-C 停）"
	cd "$TEST" && exec ./cyberstrike-ai -config config.yaml
	;;
*)
	die "未知子命令：$1（可用 sync | verify | gates | run）"
	;;
esac
