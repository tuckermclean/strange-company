#!/bin/sh
# F08 S2 review 2 (stub engine): values forwarded by the wrapper cannot inject argv words or -e forwards (plan 02
# section 14.5), and the pipeline flags work before the command (plan 50 section 7.1; plan 19 and the procedures use
# ./bootstrap --from-phase seed).
. "$(dirname "$0")/lib-s3.sh"
echo "stub-f08-review2 (value injection, pipeline flags before the command)"

fx=$TMP/repo
mkrepo "$fx"
IMG_RE='localhost/strange-company-toolbox:in-[0-9a-f]{12}'
words_after_image() { run_line | sed -E "s#^.* $IMG_RE( |\$)##"; }

# An engine front that records one "A:<arg>" line per argument; an argument with an embedded newline leaves
# continuation lines that do not start with "A:". It then runs the normal stub engine.
ARGLOG=$TMP/argv.log
cp "$HERE/engine-stub-pull.sh" "$TMP/engine-real.sh"
for e in docker podman; do
	cat >"$STUBBIN/$e" <<EOT
#!/bin/sh
for a in "\$@"; do printf 'A:%s\n' "\$a"; done >>"$ARGLOG"
exec sh "$TMP/engine-real.sh" "\$@"
EOT
	chmod +x "$STUBBIN/$e"
done
reset_all() { : >"$STUB_LOG"; : >"$ARGLOG"; }
argv() { cat "$ARGLOG"; }
# no_arg NAME LINE: no recorded argument is exactly LINE
no_arg() {
	if [ ! -s "$ARGLOG" ]; then ok "$1 (nothing was run)"; return; fi
	if grep -Fxq -- "A:$2" "$ARGLOG"; then bad "$1" "argument [$2] was injected"; else ok "$1"; fi
}

# --- a newline in --profile is refused or passed intact, never split
reset_all
wrapx "$fx" "" --profile "$(printf 'p\n--confirm-storage\nevil')" --non-interactive preflight
case $RC in 0 | 2) ok "newline in --profile: exit $RC is refuse-or-pass" ;; *) bad "newline in --profile: exit $RC" "$ERR" ;; esac
no_arg "newline in --profile does not inject --confirm-storage" "--confirm-storage"
no_arg "newline in --profile does not inject evil" "evil"

reset_all
wrapx "$fx" "" --profile="$(printf 'p\n--confirm-storage\nevil')" preflight
no_arg "newline in --profile=V does not inject --confirm-storage" "--confirm-storage"
no_arg "newline in --profile=V does not inject evil" "evil"

reset_all
wrapx "$fx" "" --confirm-budget "$(printf '1\n--confirm-plan\nevil')" preflight
no_arg "newline in --confirm-budget does not inject --confirm-plan" "--confirm-plan"
no_arg "newline in --confirm-budget does not inject evil" "evil"

# --- a newline in a forwarded environment value cannot add a -e forward
reset_all
SC_CONFIRM_PLAN=$(printf 'x\nAWS_SECRET_ACCESS_KEY')
export SC_CONFIRM_PLAN
AWS_SECRET_ACCESS_KEY=host-secret-must-not-pass
export AWS_SECRET_ACCESS_KEY
wrapx "$fx" "" preflight
unset SC_CONFIRM_PLAN AWS_SECRET_ACCESS_KEY
case $RC in 0 | 2) ok "newline in SC_CONFIRM_PLAN: exit $RC is refuse-or-pass" ;; *) bad "newline in SC_CONFIRM_PLAN: exit $RC" "$ERR" ;; esac
no_arg "newline in SC_CONFIRM_PLAN does not forward AWS_SECRET_ACCESS_KEY" "AWS_SECRET_ACCESS_KEY"

reset_all
SC_TEST_MODE=$(printf '1\nANTHROPIC_API_KEY=x')
export SC_TEST_MODE
wrapx "$fx" "" preflight
unset SC_TEST_MODE
no_arg "newline in SC_TEST_MODE does not forward ANTHROPIC_API_KEY=x" "ANTHROPIC_API_KEY=x"

# --- an empty value does not shift the next flag into the value position
reset_all
wrapx "$fx" "" --profile "" --non-interactive preflight
if [ "$RC" -eq 2 ]; then
	ok "--profile \"\": refused with exit 2"
else
	expect_eq "--profile \"\": exit" 0 "$RC"
	v=$(grep -A1 -Fx -- 'A:--profile' "$ARGLOG" | tail -n 1)
	expect_eq "--profile \"\": the value position holds the empty value, not the next flag" "A:" "$v"
fi

# --- pipeline flags before the command
for pair in "--from-phase seed" "--retry-phase provision"; do
	flag=${pair%% *}
	val=${pair#* }
	reset_all
	wrapx "$fx" "" "$flag" "$val"
	expect_eq "./bootstrap $flag $val: exit 0" 0 "$RC"
	expect_nomatch "./bootstrap $flag $val: not an unknown command" "unknown command|WRAPPER_USAGE" "$ERR x"
	expect_match "./bootstrap $flag $val: reaches sc-bootstrap" "(^| )$flag( |=)$val( |\$)" "$(words_after_image)"
	expect_eq "./bootstrap $flag $val: one container" 1 "$(runs)"

	reset_all
	wrapx "$fx" "" --non-interactive "$flag" "$val"
	expect_eq "./bootstrap --non-interactive $flag $val: exit 0" 0 "$RC"
	W=$(words_after_image)
	expect_match "global flag and $flag both reach sc-bootstrap" "(^| )--non-interactive( |\$)" "$W"
	expect_match "$flag $val reaches sc-bootstrap after a global flag" "(^| )$flag( |=)$val( |\$)" "$W"
done

finish
