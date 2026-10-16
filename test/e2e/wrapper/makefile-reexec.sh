#!/bin/sh
# 01-AC-5: Makefile re-exec contract (plan 01 section 8.1). Needs GNU make, no engine.
. "$(dirname "$0")/lib.sh"
echo "makefile-reexec (01-AC-5)"

cd "$SC_ROOT" || exit 1
RC=0
OUT=$(env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKEOVERRIDES make -n verify 2>"$TMP/err") || RC=$?
expect_eq "01-AC-5 make -n verify (SC_IN_CONTAINER unset) exits 0" 0 "$RC"
n=$(printf '%s\n' "$OUT" | grep -c . || true)
expect_eq "01-AC-5 prints exactly one command" 1 "$n"
expect_match "01-AC-5 the command starts with ./bootstrap exec --image ci -- make verify" '^\./bootstrap exec --image ci -- make verify' "$OUT"

# 01-AC-5 (review): the re-exec line is exactly the command, with no trailing whitespace, and a command-line
# SC_IN_CONTAINER is not forwarded to the container.
RC=0
OUT=$(env -u SC_IN_CONTAINER make -n verify 2>"$TMP/err") || RC=$?
expect_eq "01-AC-5 re-exec line is exactly ./bootstrap exec --image ci -- make verify" "./bootstrap exec --image ci -- make verify" "$OUT"
RC=0
OUT=$(env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES make -n verify SC_IN_CONTAINER=0 2>"$TMP/err") || RC=$?
expect_eq "01-AC-5 make -n verify SC_IN_CONTAINER=0 exits 0" 0 "$RC"
expect_nomatch "01-AC-5 SC_IN_CONTAINER=0 on the command line is not forwarded" 'SC_IN_CONTAINER=' "$OUT"
expect_eq "01-AC-5 re-exec line with SC_IN_CONTAINER=0 is exactly the plain command" "./bootstrap exec --image ci -- make verify" "$OUT"

RC=0
OUT=$(SC_IN_CONTAINER=1 make -n verify 2>"$TMP/err") || RC=$?
expect_eq "01-AC-5 make -n verify (SC_IN_CONTAINER=1) exits 0" 0 "$RC"
expect_nomatch "01-AC-5 no re-exec inside the container" '\./bootstrap exec' "$OUT"
for t in toolbox-check layout-check locks-check lint; do
	expect_match "01-AC-5 tier-V sequence includes $t" "(^|[^A-Za-z0-9_-])$t([^A-Za-z0-9_-]|\$)" "$OUT"
done

# 01-AC-5 (S2 follow-up): make variable overrides reach ./bootstrap as one argv element with the exact value,
# whatever shell metacharacters they hold. A stub ./bootstrap records its argv, one element per line.
FX=$TMP/reexec
mkdir -p "$FX"
cp "$SC_ROOT/Makefile" "$FX/Makefile"
cat >"$FX/bootstrap" <<'STUB'
#!/bin/sh
: >"$REEXEC_REC"
for a in "$@"; do printf '%s\n' "$a" >>"$REEXEC_REC"; done
STUB
chmod +x "$FX/bootstrap"
REEXEC_REC=$TMP/reexec.argv
export REEXEC_REC
check_override() { # name value
	_val=$2
	for _mode in dry real; do
		rm -f "$REEXEC_REC"
		_rc=0
		if [ "$_mode" = dry ]; then
			_line=$(cd "$FX" && env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES make -n test "GO_TEST_RUN=$_val" 2>"$TMP/err") || _rc=$?
			# the printed command is what a shell would run: execute it against the stub
			[ "$_rc" -ne 0 ] || (cd "$FX" && sh -c "$_line") >/dev/null 2>&1 || true
		else
			(cd "$FX" && env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES make test "GO_TEST_RUN=$_val") >/dev/null 2>"$TMP/err" || _rc=$?
		fi
		_want=$(printf 'exec\n--image\nci\n--\nmake\ntest\nGO_TEST_RUN=%s' "$_val")
		_got=
		[ ! -f "$REEXEC_REC" ] || _got=$(cat "$REEXEC_REC")
		expect_eq "01-AC-5 $_mode: $1 reaches ./bootstrap as one argv element" "$_want" "$_got"
	done
}
check_override "whitespace run and tab" 'a  b	c'
check_override "alternation" 'TestA|TestB'
check_override "trailing dollar" 'TestA$'
check_override "anchored group" '^(A|B)$'

# 01-AC-5 (S4 review): the printed re-exec line keeps whitespace inside a quoted override value exactly
# (a double space and a tab), and without overrides the line has no trailing whitespace.
WS=$(printf 'a  b\tc')
RC=0
OUT=$(env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES make -n verify "GO_TEST_RUN=$WS" 2>"$TMP/err") || RC=$?
expect_eq "01-AC-5 make -n verify with a whitespace override exits 0" 0 "$RC"
expect_eq "01-AC-5 re-exec line keeps the override's double space and tab inside its quotes" \
	"./bootstrap exec --image ci -- make verify 'GO_TEST_RUN=$WS'" "$OUT"
OUT=$(env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES make -n verify 2>"$TMP/err") || RC=$?
expect_eq "01-AC-5 no-override line is exactly the command, no trailing whitespace" "./bootstrap exec --image ci -- make verify" "$OUT"

finish
