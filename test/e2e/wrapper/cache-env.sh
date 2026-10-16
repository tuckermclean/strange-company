#!/bin/sh
# 01-AC-6, 52-AC-5: every ./bootstrap exec --image ci container defaults GOMODCACHE and GOCACHE to the
# repo-local .cache (plan 01 section 8.1, plan 02 section 7.2), so a warm-up in one container survives for
# the next. Engine-free: the default may live in ./bootstrap (as -e flags) or in toolbox/bin/sc-entrypoint.
. "$(dirname "$0")/lib.sh"
echo "cache-env (01-AC-6, 52-AC-5)"

files="$SC_ROOT/bootstrap $SC_ROOT/toolbox/bin/sc-entrypoint"
# shellcheck disable=SC2086 # files is a fixed list of two paths without spaces
mod=$(grep -hE '(^|[^A-Za-z_])GOMODCACHE' $files 2>/dev/null | grep -E 'SC_REPO_ROOT|REPO' | grep -F '.cache/go/mod' || true)
# shellcheck disable=SC2086
build=$(grep -hE '(^|[^A-Za-z_])GOCACHE' $files 2>/dev/null | grep -E 'SC_REPO_ROOT|REPO' | grep -F '.cache/go/build' || true)
[ -n "$mod" ] && ok "01-AC-6 GOMODCACHE defaults to \$SC_REPO_ROOT/.cache/go/mod" ||
	bad "01-AC-6 GOMODCACHE defaults to \$SC_REPO_ROOT/.cache/go/mod" "neither bootstrap nor toolbox/bin/sc-entrypoint defines it"
[ -n "$build" ] && ok "01-AC-6 GOCACHE defaults to \$SC_REPO_ROOT/.cache/go/build" ||
	bad "01-AC-6 GOCACHE defaults to \$SC_REPO_ROOT/.cache/go/build" "neither bootstrap nor toolbox/bin/sc-entrypoint defines it"

finish
