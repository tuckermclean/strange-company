#!/bin/sh
# F08 S2 (stub engine): a bare ./bootstrap means run (plan 50 section 7.1), the top-level global flags reach
# sc-bootstrap, and the runtime-only variables of plan 50 section 9.2 (SC_TEST_*, SC_CONFIRM_*, ...) and
# SC_RECOVERY_PASSPHRASE_FILE (plan 02 section 14.5) reach the container. Nothing else is forwarded.
. "$(dirname "$0")/lib-s3.sh"
echo "stub-f08-wrapper (50-AC-7 CLI leg passthrough, 50-AC-15, 50-AC-31, 50-AC-36 flags, plan 50 7.1)"

fx=$TMP/repo
mkrepo "$fx"
HOME_=$TMP/home
mkdir -p "$HOME_"
IMG_RE='localhost/strange-company-toolbox:in-[0-9a-f]{12}'
# words_after_image: what follows the image reference on the last run line
words_after_image() { run_line | sed -E "s#^.* $IMG_RE( |\$)##"; }
has_word() { # name WORD-REGEX LINE
	expect_match "$1" "(^| )$2( |\$)" "$3"
}

# --- bare ./bootstrap is run
reset_log
wrapx "$fx" "" 
expect_eq "bare ./bootstrap exits 0" 0 "$RC"
expect_nomatch "bare ./bootstrap is not a usage error" "missing command|WRAPPER_USAGE" "$ERR x"
expect_eq "bare ./bootstrap hands 'run' to sc-bootstrap" "run" "$(words_after_image)"
expect_eq "bare ./bootstrap starts one container" 1 "$(runs)"

# --- top-level global flags, with and without a verb
reset_log
wrapx "$fx" "" --non-interactive --profile small-ha
expect_eq "flags only: exit 0" 0 "$RC"
W=$(words_after_image)
has_word "flags only: the verb is run" "run" "$W"
has_word "flags only: --non-interactive reaches sc-bootstrap" "--non-interactive" "$W"
expect_match "flags only: --profile small-ha reaches sc-bootstrap" "(^| )--profile( |=)small-ha( |\$)" "$W"

reset_log
wrapx "$fx" "" --non-interactive --log-format json -v --verbose --profile self-hosting-minimum preflight
expect_eq "global flags before a verb: exit 0" 0 "$RC"
W=$(words_after_image)
has_word "global flags: the verb preflight" "preflight" "$W"
has_word "global flags: --non-interactive" "--non-interactive" "$W"
expect_match "global flags: --log-format json" "(^| )--log-format( |=)json( |\$)" "$W"
has_word "global flags: -v" "-v" "$W"
has_word "global flags: --verbose" "--verbose" "$W"
expect_match "global flags: --profile" "(^| )--profile( |=)self-hosting-minimum( |\$)" "$W"
expect_eq "global flags: one container" 1 "$(runs)"

reset_log
wrapx "$fx" "" --confirm-plan ab12cd34 --confirm-storage lab-one --confirm-budget 137.50 --confirm-backstops /x/b.yaml run
expect_eq "confirmation flags before the verb: exit 0" 0 "$RC"
W=$(words_after_image)
for pair in "--confirm-plan ab12cd34" "--confirm-storage lab-one" "--confirm-budget 137.50" "--confirm-backstops /x/b.yaml"; do
	flag=${pair%% *}
	val=${pair#* }
	expect_match "confirmation flag $flag reaches sc-bootstrap" "(^| )$flag( |=)$val( |\$)" "$W"
done

# flags after the verb keep working (unchanged behaviour)
reset_log
wrapx "$fx" "" preflight --profile small-ha --non-interactive
expect_eq "flags after the verb: exit 0" 0 "$RC"
W=$(words_after_image)
has_word "flags after the verb: preflight" "preflight" "$W"
has_word "flags after the verb: --non-interactive" "--non-interactive" "$W"

# --env-file stays a wrapper flag and still combines with the new ones
printf 'CLUSTER_NAME=lab-one\n' >"$TMP/f08.env"
reset_log
wrapx "$fx" "" --env-file "$TMP/f08.env" --non-interactive preflight
expect_eq "--env-file with a global flag: exit 0" 0 "$RC"
expect_match "--env-file is mounted as the container's bootstrap.env" "$(mount_re "$TMP/f08.env" /run/sc-input/bootstrap.env ro)" "$(run_line)"
has_word "--env-file with a global flag: --non-interactive reaches sc-bootstrap" "--non-interactive" "$(words_after_image)"

# unknown or incomplete global flags are still usage errors
for bad in "--bogus preflight" "--profile" "--log-format" "--log-format xml preflight"; do
	reset_log
	# shellcheck disable=SC2086 # the words are separate arguments by design
	wrapx "$fx" "" $bad
	expect_eq "usage error for [$bad]: exit 2" 2 "$RC"
	expect_eq "usage error for [$bad]: no container started" 0 "$(runs)"
done

# --- runtime-only variables reach the container (plan 50 section 9.2) when set
reset_log
wrapx "$fx" "SC_TEST_MODE=1 SC_TEST_CRASH_AT=plan.storage-apply:after SC_TEST_STOP_AFTER=configure SC_CONFIRM_PLAN=ab12cd34 SC_CONFIRM_STORAGE=lab-one SC_CONFIRM_BUDGET=137.50 SC_VERIFY_MEMORY_WINDOW=45m SC_VERIFY_SETTLE=5m SC_VERIFY_RECONCILE_TIMEOUT=90m SC_ACCEPT_STALE_PRICES=1 SC_LAB_S3_INCLUSTER_ENDPOINT=https://s3.lab.invalid" preflight
expect_eq "runtime-only variables: exit 0" 0 "$RC"
L=$(run_line)
for kv in SC_TEST_MODE=1 SC_TEST_CRASH_AT=plan.storage-apply:after SC_TEST_STOP_AFTER=configure SC_CONFIRM_PLAN=ab12cd34 \
	SC_CONFIRM_STORAGE=lab-one SC_CONFIRM_BUDGET=137.50 SC_VERIFY_MEMORY_WINDOW=45m SC_VERIFY_SETTLE=5m \
	SC_VERIFY_RECONCILE_TIMEOUT=90m SC_ACCEPT_STALE_PRICES=1 SC_LAB_S3_INCLUSTER_ENDPOINT=https://s3.lab.invalid; do
	expect_match "runtime-only $kv is passed with -e" "(-e|--env)[ =]$kv( |\$)" "$L"
done

# --- ... and nothing else from the host environment, and nothing when unset
reset_log
wrapx "$fx" "SC_UNRELATED_X=1 AWS_SECRET_ACCESS_KEY=host-secret-must-not-pass S3_SECRET_ACCESS_KEY=host-secret-must-not-pass ANTHROPIC_API_KEY=host-secret-must-not-pass" preflight
expect_eq "unrelated host variables: exit 0" 0 "$RC"
L=$(run_line)
expect_nomatch "SC_UNRELATED_X is not forwarded" "SC_UNRELATED_X" "$L"
expect_nomatch "no host credential is forwarded" "host-secret-must-not-pass|AWS_SECRET_ACCESS_KEY|S3_SECRET_ACCESS_KEY|ANTHROPIC_API_KEY" "$L"
expect_nomatch "unset runtime-only variables are not forwarded, not even empty" "SC_TEST_|SC_CONFIRM_|SC_RECOVERY_PASSPHRASE_FILE|SC_VERIFY_|SC_ACCEPT_STALE_PRICES|SC_LAB_S3_" "$L"

# --- SC_RECOVERY_PASSPHRASE_FILE: the file is mounted read-only and the variable is rewritten (plan 02 section 14.5)
printf 'correct horse battery staple\n' >"$TMP/passphrase"
chmod 600 "$TMP/passphrase"
reset_log
wrapx "$fx" "SC_RECOVERY_PASSPHRASE_FILE=$TMP/passphrase" preflight
expect_eq "SC_RECOVERY_PASSPHRASE_FILE: exit 0" 0 "$RC"
L=$(run_line)
expect_match "the passphrase file is mounted read-only at /run/sc-input/passphrase" "$(mount_re "$TMP/passphrase" /run/sc-input/passphrase ro)" "$L"
expect_match "the variable is rewritten to the container path" "(-e|--env)[ =]SC_RECOVERY_PASSPHRASE_FILE=/run/sc-input/passphrase( |\$)" "$L"
expect_nomatch "the host path is not passed as the variable's value" "SC_RECOVERY_PASSPHRASE_FILE=$TMP/passphrase" "$L"
expect_nomatch "the passphrase itself never appears" "correct horse" "$L"

finish
