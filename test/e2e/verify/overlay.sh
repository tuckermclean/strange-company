# shellcheck shell=sh
# Overlay of a checkout's uncommitted work onto a fresh clone of it (sourced by test/e2e/verify/run and
# tested by test/e2e/wrapper/overlay.sh). POSIX sh.
#   sc_overlay <orig> <clone>
# The path list is every path that differs from HEAD in the index or the working tree (renames split into
# delete + add) plus untracked-not-ignored paths, NUL-separated so any file name survives. A path present in
# <orig> (including a dangling symlink) is copied with `cp -P -p`, so symlinks stay symlinks; an absent path is
# removed from the clone. Returns 2 when the list cannot be built or any copy or removal fails.

sc_overlay() {
	_o=$1
	_c=$2
	_l=$(mktemp) || return 2
	if ! { git -C "$_o" diff -z --name-only --no-renames HEAD && git -C "$_o" ls-files -z -o --exclude-standard; } >"$_l"; then
		rm -f "$_l"
		return 2
	fi
	# shellcheck disable=SC2016 # the inner script is meant to expand in the child shell
	xargs -0 sh -c '
		o=$1 c=$2
		shift 2
		rc=0
		for f; do
			if [ -e "$o/$f" ] || [ -L "$o/$f" ]; then
				[ -d "$o/$f" ] && [ ! -L "$o/$f" ] && continue # gitlink or empty directory: nothing to copy
				mkdir -p "$c/$(dirname "$f")" && rm -rf "$c/$f" && cp -P -p "$o/$f" "$c/$f" || {
					printf "overlay: cannot copy %s\n" "$f" >&2
					rc=1
				}
			else
				rm -rf "$c/$f" || {
					printf "overlay: cannot remove %s\n" "$f" >&2
					rc=1
				}
			fi
		done
		exit "$rc"
	' sh "$_o" "$_c" <"$_l"
	_r=$?
	rm -f "$_l"
	[ "$_r" -eq 0 ] || return 2
	return 0
}
