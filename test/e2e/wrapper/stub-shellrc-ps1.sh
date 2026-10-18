#!/bin/sh
# F03 S3 review round 2 (N1, N2, N4): toolbox/shellrc prompt checks that do not depend on the terminal or the working
# directory. The prompt is read with `bash -i -c '${PS1@P}'` (the shell's own prompt expansion, no readline redisplay),
# from a short and from a very long working directory. Covers the cluster name grammar shared with the wrapper's envval
# (N4), the kube context (N2), and ${...}, !, $(...) and backslash cases (N2, 02-AC-11).
. "$(dirname "$0")/lib.sh"
echo "stub-shellrc-ps1 (02-R-6, 02-AC-11 prompt, S3 review 2 N1 N2 N4)"

command -v bash >/dev/null 2>&1 || { bad "bash is installed"; finish; exit $?; }
RC_FILE=$SC_ROOT/toolbox/shellrc
IN=$TMP/in
mkdir -p "$IN" "$TMP/run" "$TMP/kbin"
long=$TMP
i=0
while [ "$i" -lt 12 ]; do
	long=$long/very-long-directory-name-to-push-the-prompt-past-any-terminal-width
	i=$((i + 1))
done
mkdir -p "$long"
cat >"$TMP/kbin/kubectl" <<'EOT'
#!/bin/sh
[ "$1 $2" = "config current-context" ] || exit 1
printf '%s\n' "$STUB_KUBE_CONTEXT"
EOT
chmod +x "$TMP/kbin/kubectl"

# prompt_for DIR 'ENV FILE TEXT' [KUBE_CONTEXT]: sets PROMPT to the expanded PS1 (the text between the markers).
prompt_for() {
	printf '%s' "$2" >"$IN/bootstrap.env"
	if [ -n "${3:-}" ]; then
		(cd "$1" && env -i PATH="$TMP/kbin:$PATH" HOME="$TMP/h" SC_INPUT_DIR="$IN" SC_RUN_DIR="$TMP/run" TERM=dumb \
			KUBECONFIG="$TMP/kubeconfig" STUB_KUBE_CONTEXT="$3" \
			bash --rcfile "$RC_FILE" -i -c 'printf "@@PS1<%s>@@\n" "${PS1@P}"' </dev/null >"$TMP/sh.out" 2>"$TMP/sh.err") || true
	else
		(cd "$1" && env -i PATH="$TMP/kbin:$PATH" HOME="$TMP/h" SC_INPUT_DIR="$IN" SC_RUN_DIR="$TMP/run" TERM=dumb \
			bash --rcfile "$RC_FILE" -i -c 'printf "@@PS1<%s>@@\n" "${PS1@P}"' </dev/null >"$TMP/sh.out" 2>"$TMP/sh.err") || true
	fi
	PROMPT=$(sed -n 's/^.*@@PS1<\(.*\)>@@$/\1/p' "$TMP/sh.out" | head -n 1)
}
# both DESC 'ENV TEXT' PATTERN [KUBE_CONTEXT]: the prompt matches PATTERN from both directories
both() {
	for _d in "$TMP" "$long"; do
		_n=short
		[ "$_d" != "$long" ] || _n=long-cwd
		prompt_for "$_d" "$2" "${4:-}"
		expect_match "$1 ($_n)" "$3" "$PROMPT"
	done
}
# sanity: the harness reads a prompt at all
prompt_for "$TMP" 'CLUSTER_NAME=demo
'
expect_match "harness: the expanded prompt is captured" '^\[sc ' "$PROMPT"

# --- the cluster name follows the env-file grammar (N4): whitespace trimmed, one quote level, last assignment wins
both "N4 CLUSTER_NAME=demo" 'CLUSTER_NAME=demo
' '^\[sc demo\|none\] '
both "N4 double quotes are removed" 'CLUSTER_NAME="demo"
' '^\[sc demo\|none\] '
both "N4 single quotes are removed" "CLUSTER_NAME='demo'
" '^\[sc demo\|none\] '
both "N4 surrounding whitespace is trimmed" 'CLUSTER_NAME=  demo 	
' '^\[sc demo\|none\] '
both "N4 a CRLF line ending is tolerated" "$(printf 'CLUSTER_NAME=demo\r\n')
" '^\[sc demo\|none\] '
both "N4 the last assignment wins" 'CLUSTER_NAME=first
CLUSTER_NAME=second
' '^\[sc second\|none\] '
both "N4 a later empty assignment unsets" 'CLUSTER_NAME=first
CLUSTER_NAME=
' '^\[sc no-cluster\|none\] '
# One quote level: '"demo"' leaves "demo" with its double quotes, which is not a name, so no name is shown.
both "N4 only one quote level is removed" "CLUSTER_NAME='\"demo\"'
" '^\[sc no-cluster\|none\] '
both "N4 a comment line assigns nothing" '#CLUSTER_NAME=nope
' '^\[sc no-cluster\|none\] '

# --- hostile cluster names are inert (02-AC-11, N2)
M=$TMP/marker
for v in "\$(touch $M)" "\`touch $M\`" '${HOME}' '${HOME:-x}' 'x!y' 'x\!y' 'x\h\u' 'a;b' '$((1+1))'; do
	rm -f "$M"
	for _d in "$TMP" "$long"; do
		prompt_for "$_d" "CLUSTER_NAME=$v
"
		expect_match "N2 CLUSTER_NAME=$v still gives the sc prompt" '^\[sc [^]|]*\|none\] ' "$PROMPT"
		expect_nomatch "N2 CLUSTER_NAME=$v: no expansion of HOME, no history number, no host or user name" "$TMP/h|\\[sc x[0-9]|\\[sc x$(id -un)" "$PROMPT"
		if [ -e "$M" ]; then bad "N2 CLUSTER_NAME=$v executes nothing"; rm -f "$M"; else ok "N2 CLUSTER_NAME=$v executes nothing"; fi
	done
done

# --- the kube context comes from kubectl only with KUBECONFIG set, and is shown inertly (N2)
both "N2 the context of kubectl is shown when KUBECONFIG is set" 'CLUSTER_NAME=demo
' '^\[sc demo\|kind-demo\] ' kind-demo
prompt_for "$TMP" 'CLUSTER_NAME=demo
'
expect_match "N2 without KUBECONFIG the context is none (kubectl is not consulted)" '^\[sc demo\|none\] ' "$PROMPT"
rm -f "$M"
for c in "ctx-\$(touch $M)" "ctx-\`touch $M\`" 'ctx-${HOME}' 'ctx-\h' 'ctx-!x'; do
	for _d in "$TMP" "$long"; do
		prompt_for "$_d" 'CLUSTER_NAME=demo
' "$c"
		expect_match "N2 kube context $c is shown as inert text" '^\[sc demo\|ctx-[A-Za-z0-9._@:/-]*\] ' "$PROMPT"
		expect_nomatch "N2 kube context $c: no expansion" "$TMP/h|ctx-.*$(hostname -s 2>/dev/null || hostname)" "$PROMPT"
		if [ -e "$M" ]; then bad "N2 kube context $c executes nothing"; rm -f "$M"; else ok "N2 kube context $c executes nothing"; fi
	done
done
finish
