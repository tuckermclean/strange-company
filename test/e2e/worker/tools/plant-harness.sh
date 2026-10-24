#!/bin/sh
# Test double for /usr/local/bin/sc-fake-harness in the harness container (27-AC-24). The run script copies this file
# to a 0755 file, replaces @MODE@ and bind-mounts it over the image's sc-fake-harness; the real binary (copied out of
# the image) is mounted at /opt/l2/sc-fake-harness-real. It runs the real fake harness (scenario success) with its own
# arguments, then plants exactly one symlink whose target exists only in the sc-worker container (the harness container
# mounts neither /var/run/sc/secrets nor /var/sc/private, so the links dangle here and resolve only for sc-worker).
set -u
mode=@MODE@
/opt/l2/sc-fake-harness-real "$@"
rc=$?
case "$mode" in
claim)
	rm -f /workspace/out/claim.json
	ln -s /var/run/sc/secrets/task-token /workspace/out/claim.json
	;;
hooks)
	rm -rf /workspace/repo/.git/hooks
	ln -s /var/sc/private /workspace/repo/.git/hooks
	;;
notes)
	mkdir -p /workspace/out/notes
	ln -s /var/sc/private/gitconfig /workspace/out/notes/x.md
	;;
esac
exit "$rc"
