#!/bin/sh
# Overlay fidelity for test/e2e/verify/run (review follow-up): the clone made from a checkout must match the
# checkout's working tree, including staged renames, symlinks, non-ASCII names, modifications and deletions.
. "$(dirname "$0")/lib.sh"
echo "overlay (verify e2e checkout copy)"

o=$TMP/ov-orig
c=$TMP/ov-clone
mkdir -p "$o"
g() { git -C "$o" -c user.email=t@example.invalid -c user.name=t "$@"; }
g init -q
printf 'one\n' >"$o/renamed-old.txt"
printf 'keep\n' >"$o/modified.txt"
printf 'gone\n' >"$o/deleted.txt"
mkdir -p "$o/sub"
printf 'x\n' >"$o/sub/inner.txt"
g add -A
g commit -q -m base

g mv renamed-old.txt renamed-new.txt # staged rename
printf 'changed\n' >"$o/modified.txt"
rm "$o/deleted.txt"
ln -s modified.txt "$o/link-file"
ln -s sub "$o/link-dir"
printf 'u\n' >"$o/café.txt"

git clone -q --no-local "$o" "$c" || exit 2
# shellcheck source=test/e2e/verify/overlay.sh
. "$SC_ROOT/test/e2e/verify/overlay.sh"
RC=0
sc_overlay "$o" "$c" || RC=$?
expect_eq "overlay exits 0" 0 "$RC"

[ -f "$c/renamed-new.txt" ] && [ ! -e "$c/renamed-old.txt" ] && r=applied || r=missing
expect_eq "staged rename applied (new present, old absent)" applied "$r"
expect_eq "modified tracked file copied" changed "$(cat "$c/modified.txt" 2>/dev/null)"
[ ! -e "$c/deleted.txt" ] && r=absent || r=present
expect_eq "deleted tracked file absent" absent "$r"
[ -L "$c/link-file" ] && r=symlink || r=not-symlink
expect_eq "untracked file symlink stays a symlink" symlink "$r"
expect_eq "file symlink target preserved" modified.txt "$(readlink "$c/link-file" 2>/dev/null)"
[ -L "$c/link-dir" ] && r=symlink || r=not-symlink
expect_eq "untracked directory symlink stays a symlink" symlink "$r"
expect_eq "directory symlink target preserved" sub "$(readlink "$c/link-dir" 2>/dev/null)"
[ -f "$c/café.txt" ] && r=present || r=missing
expect_eq "non-ASCII file name present" present "$r"

# A failed copy is reported with exit 2 (the destination parent is a file).
mkdir -p "$o/blocker"
printf 'b\n' >"$o/blocker/f.txt"
printf 'not a dir\n' >"$c/blocker"
RC=0
sc_overlay "$o" "$c" 2>/dev/null || RC=$?
expect_eq "failed copy returns 2" 2 "$RC"

finish
