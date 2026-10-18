#!/bin/sh
# F03 S4 review round 1 (02-R-6, 02-AC-11): toolbox/shellrc keeps the leading run of ASCII name characters of
# CLUSTER_NAME. Under a UTF-8 locale `[A-Za-z0-9._-]` must not match non-ASCII letters, so the filter needs LC_ALL=C.
. "$(dirname "$0")/lib.sh"
echo "stub-shellrc-locale (02-R-6, 02-AC-11 prompt, ASCII-only cluster name under a UTF-8 locale)"

command -v bash >/dev/null 2>&1 || { bad "bash is installed"; finish; exit $?; }
# A locale whose bracket ranges collate (en_US.UTF-8) is compiled into $TMP when localedef and the source are present
# (glibc's C.UTF-8 ranges are codepoint ranges and would hide the defect); otherwise an installed UTF-8 locale is used.
loc=
LOCPATH_T=
if command -v localedef >/dev/null 2>&1 && mkdir -p "$TMP/loc" &&
	localedef -i en_US -f UTF-8 "$TMP/loc/en_US.UTF-8" >/dev/null 2>&1 && LOCPATH=$TMP/loc LC_ALL=en_US.UTF-8 locale charmap 2>/dev/null | grep -q UTF-8; then
	loc=en_US.UTF-8
	LOCPATH_T=$TMP/loc
else
	for l in en_US.UTF-8 en_US.utf8 C.UTF-8 C.utf8; do
		if LC_ALL=$l locale charmap 2>/dev/null | grep -q UTF-8 && locale -a 2>/dev/null | grep -qix "$l"; then loc=$l; break; fi
	done
fi
[ -n "$loc" ] || { echo "  SKIP no UTF-8 locale available"; finish; exit $?; }
RC_FILE=$SC_ROOT/toolbox/shellrc
IN=$TMP/in
mkdir -p "$IN" "$TMP/run" "$TMP/h"

# prompt_for 'ENV FILE TEXT': PROMPT is the expanded PS1 under the UTF-8 locale.
prompt_for() {
	printf '%s' "$1" >"$IN/bootstrap.env"
	(cd "$TMP" && env -i PATH="$PATH" HOME="$TMP/h" SC_INPUT_DIR="$IN" SC_RUN_DIR="$TMP/run" TERM=dumb LC_ALL="$loc" ${LOCPATH_T:+LOCPATH="$LOCPATH_T"} \
		bash --rcfile "$RC_FILE" -i -c 'printf "@@PS1<%s>@@\n" "${PS1@P}"' </dev/null >"$TMP/sh.out" 2>"$TMP/sh.err") || true
	PROMPT=$(sed -n 's/^.*@@PS1<\(.*\)>@@$/\1/p' "$TMP/sh.out" | head -n 1)
}

# Deterministic on every host (also where no collating locale can be built): a recording sed shim shows the
# environment of the name-character filter, the sed whose script holds the bracket expression.
mkdir -p "$TMP/shim"
REAL_SED=$(command -v sed)
cat >"$TMP/shim/sed" <<EOT
#!/bin/sh
case "\$*" in *A-Za-z0-9*) printf '%s\\n' "\${LC_ALL-unset}" >>"$TMP/sed-lc.log" ;; esac
exec $REAL_SED "\$@"
EOT
chmod +x "$TMP/shim/sed"
: >"$TMP/sed-lc.log"
SHIM_PATH=$TMP/shim:$PATH
saved_path=$PATH
PATH=$SHIM_PATH
prompt_for 'CLUSTER_NAME=prödx
'
PATH=$saved_path
expect_eq "02-R-6 the name-character filter runs with LC_ALL=C under $loc" "C" "$(sort -u "$TMP/sed-lc.log" | tr '\n' ' ' | sed 's/ $//')"
prompt_for 'CLUSTER_NAME=prödx
'
expect_match "02-R-6 harness: the prompt is captured ($loc)" '^\[sc ' "$PROMPT"
expect_match "02-R-6 CLUSTER_NAME=prödx under $loc: the name is cut at the first non-ASCII letter" '^\[sc pr\|none\] ' "$PROMPT"
prompt_for 'CLUSTER_NAME=édemo
'
expect_match "02-R-6 CLUSTER_NAME=édemo under $loc: a leading non-ASCII letter leaves no name" '^\[sc no-cluster\|none\] ' "$PROMPT"
prompt_for 'CLUSTER_NAME=demo
'
expect_match "02-R-6 an ASCII name is unchanged under $loc" '^\[sc demo\|none\] ' "$PROMPT"
finish
