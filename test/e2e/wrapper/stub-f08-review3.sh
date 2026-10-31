#!/bin/sh
# F08 S3 (stub engine): `./bootstrap exec -- -x` runs -x inside the container (plan 02 section 7.1); the entrypoint
# must not hand a flag-looking exec command to sc-bootstrap as if it were a global flag (plan 50 section 7.1).
. "$(dirname "$0")/lib-s3.sh"
echo "stub-f08-review3 (exec -- -x is a command, not an sc-bootstrap flag)"

fx=$TMP/repo
mkrepo "$fx"
IMG_RE='localhost/strange-company-(toolbox|ci):in-[0-9a-f]{12}'
words_after_image() { run_line | sed -E "s#^.* $IMG_RE( |\$)##"; }
cp "$HERE/engine-stub-pull.sh" "$TMP/engine-real.sh"
for e in docker podman; do
	cat >"$STUBBIN/$e" <<EOT
#!/bin/sh
exec sh "$TMP/engine-real.sh" "\$@"
EOT
	chmod +x "$STUBBIN/$e"
done

# the wrapper marks an exec command that starts with "-" with the explicit "exec --", like the verb names
for cmd in "-x" "--version" "-c echo"; do
	: >"$STUB_LOG"
	# shellcheck disable=SC2086 # CMD is a space-separated word list
	wrapx "$fx" "" exec -- $cmd
	expect_eq "./bootstrap exec -- $cmd: exit 0" 0 "$RC"
	expect_eq "./bootstrap exec -- $cmd: the container is told exec -- first" "exec -- $cmd" "$(words_after_image)"
done
: >"$STUB_LOG"
wrapx "$fx" "" exec --image ci -- -x
expect_eq "./bootstrap exec --image ci -- -x: exit 0" 0 "$RC"
expect_eq "./bootstrap exec --image ci -- -x: exec -- -x reaches the container" "exec -- -x" "$(words_after_image)"

# an ordinary command is unchanged
: >"$STUB_LOG"
wrapx "$fx" "" exec -- git status
expect_eq "./bootstrap exec -- git status: words after the image" "git status" "$(words_after_image)"

# the entrypoint: a flag-looking exec command is run, never given to sc-bootstrap
EB=$TMP/ebin
mkdir -p "$EB" "$TMP/run" "$TMP/in"
cat >"$EB/sc-bootstrap" <<'EOT'
#!/bin/sh
printf '%s\n' "$*" >>"$CALLS"
EOT
chmod +x "$EB/sc-bootstrap"
CALLS=$TMP/ep-calls
: >"$CALLS"
(cd "$EB" && env -i PATH="$EB:/usr/bin:/bin" CALLS="$CALLS" SC_REPO_ROOT="$TMP" SC_RUN_DIR="$TMP/run" SC_INPUT_DIR="$TMP/in" \
	sh "$SC_ROOT/toolbox/bin/sc-entrypoint" exec -- -x >/dev/null 2>&1) || true
expect_eq "entrypoint: only toolbox init reached sc-bootstrap, not -x" "toolbox init" "$(cat "$CALLS")"

finish
