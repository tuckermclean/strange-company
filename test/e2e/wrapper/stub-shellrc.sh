#!/bin/sh
# F03 S3 review round 1, blocker (02-R-6, 02-AC-11): toolbox/shellrc reads CLUSTER_NAME from the env file for the
# prompt; bash prompt expansion must never execute $(...) or backticks from that value.
. "$(dirname "$0")/lib.sh"
echo "stub-shellrc (02-R-6, 02-AC-11 prompt)"

command -v bash >/dev/null 2>&1 || { bad "bash is installed"; finish; exit $?; }
RC_FILE=$SC_ROOT/toolbox/shellrc
IN=$TMP/in
mkdir -p "$IN" "$TMP/run"

# prompt_for 'CLUSTER_NAME line': runs the interactive shell; sets PROMPT (stderr, where bash prints prompts).
prompt_for() {
	printf '%s\n' "$1" >"$IN/bootstrap.env"
	# cwd-independent: a long cwd in the default prompt makes TERM=dumb readline scroll and hide "[sc ".
	(cd "$TMP" && env -i PATH="$PATH" HOME="$TMP" SC_INPUT_DIR="$IN" SC_RUN_DIR="$TMP/run" TERM=dumb COLUMNS=1000 \
		bash --rcfile "$RC_FILE" -i </dev/null >"$TMP/sh.out" 2>"$TMP/sh.err") || true
	PROMPT=$(cat "$TMP/sh.err")
}

M1=$TMP/marker1
M2=$TMP/marker2
M3=$TMP/marker3
rm -f "$M1" "$M2" "$M3"
prompt_for "CLUSTER_NAME=\$(touch $M1)"
if [ -e "$M1" ]; then bad "02-AC-11 CLUSTER_NAME=\$(...) is not executed by prompt expansion"; else ok "02-AC-11 CLUSTER_NAME=\$(...) is not executed by prompt expansion"; fi
expect_match "02-R-6 the prompt is still shown with the sc prefix" '\[sc ' "$PROMPT"
prompt_for "CLUSTER_NAME=\`touch $M2\`"
if [ -e "$M2" ]; then bad "02-AC-11 CLUSTER_NAME=backticks is not executed by prompt expansion"; else ok "02-AC-11 CLUSTER_NAME=backticks is not executed by prompt expansion"; fi
expect_match "02-R-6 the prompt is still shown with the sc prefix (backticks)" '\[sc ' "$PROMPT"

# Backslash escapes (\h \u \w \$ ...) from the value are not expanded either.
host=$(hostname -s 2>/dev/null || hostname)
prompt_for 'CLUSTER_NAME=zz\h\u'
expect_match "02-R-6 the prompt shows the cluster name" 'zz' "$PROMPT"
expect_nomatch "02-R-6 a backslash escape in the value is not expanded (\\h)" "zz$host" "$PROMPT"

# A plain name still works.
prompt_for 'CLUSTER_NAME=demo'
expect_match "02-R-6 an ordinary cluster name appears in the prompt" '\[sc demo\|none\]' "$PROMPT"
rm -f "$M3"
finish
