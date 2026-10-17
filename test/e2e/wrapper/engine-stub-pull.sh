#!/bin/sh
# Stub engine for F03 S3: engine-stub.sh plus pull, save and registry-reference inspect. Installed as "docker" and
# "podman" by lib-s3.sh. Everything else is delegated to engine-stub.sh ($HERE).
# Knobs: STUB_PULL_FAIL=always | <n> (the first n pulls fail), STUB_PULLED (file of refs present locally),
# STUB_PULL_COUNT (file with one line per pull attempt).
sub=$1
[ "$sub" = image ] && sub="$1 $2"

# A registry reference: contains "/" and "@sha256:" but is not a localhost/ image.
reg_ref() {
	for a in "$@"; do
		case $a in
		localhost/*) ;;
		*/*@sha256:*) printf '%s\n' "$a" && return 0 ;;
		esac
	done
	return 1
}

case $sub in
pull | "image pull")
	printf '%s\n' "$*" >>"$STUB_LOG"
	ref=$(reg_ref "$@") || exit 1
	echo "$ref" >>"$STUB_PULL_COUNT"
	n=$(wc -l <"$STUB_PULL_COUNT" | tr -d ' ')
	case ${STUB_PULL_FAIL:-0} in
	always) exit 1 ;;
	*) [ "$n" -gt "${STUB_PULL_FAIL:-0}" ] || exit 1 ;;
	esac
	echo "$ref" >>"$STUB_PULLED"
	exit 0
	;;
save | "image save")
	printf '%s\n' "$*" >>"$STUB_LOG"
	prev=
	for a in "$@"; do
		case $prev in -o | --output) : >"$a" ;; esac
		case $a in --output=*) : >"${a#--output=}" ;; esac
		prev=$a
	done
	exit 0
	;;
inspect | "image inspect" | "image exists")
	if ref=$(reg_ref "$@"); then
		printf '%s\n' "$*" >>"$STUB_LOG"
		grep -Fxq -- "$ref" "$STUB_PULLED" || exit 1
		echo "sha256:stubpulled"
		exit 0
	fi
	;;
esac
exec sh "$HERE/engine-stub.sh" "$@"
