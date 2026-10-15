#!/bin/sh
# Stub container engine. Installed as both "docker" and "podman" on PATH by the tests.
# Records every invocation (arguments joined by single spaces) to $STUB_LOG.
# Image state lives in $STUB_STATE as "<tag>\t<inputs-id label>".
# Knobs: STUB_RUN_SLEEP=1 (+STUB_SIG_PID/GOT/CHILD), STUB_ROOTLESS=1, STUB_RUN_EXIT=<n>, STUB_BUILD_FAIL=1.
printf '%s\n' "$*" >>"$STUB_LOG"

sub=$1
[ "$sub" = image ] || [ "$sub" = container ] || [ "$sub" = buildx ] && sub="$1 $2"

ref=
for a in "$@"; do
	case $a in
	localhost/*) ref=${a%%@*} ;;
	esac
done
fmt=0
for a in "$@"; do
	case $a in --format | --format=* | -f | -q) fmt=1 ;; esac
done
label_of() { awk -F '\t' -v t="$1" '$1 == t { print $2 }' "$STUB_STATE" | tail -n 1; }

case $sub in
info)
	case "$*" in
	*SecurityOptions*)
		if [ "${STUB_ROOTLESS:-0}" = 1 ]; then echo '[name=seccomp,profile=builtin name=rootless]'; else echo '[name=seccomp,profile=builtin]'; fi
		;;
	*Rootless*)
		if [ "${STUB_ROOTLESS:-0}" = 1 ]; then echo true; else echo false; fi
		;;
	*) echo "stub engine info" ;;
	esac
	exit 0
	;;
build | "image build" | "buildx build")
	if [ "${STUB_BUILD_FAIL:-0}" = 1 ]; then
		echo "TOOLBOX_CHECKSUM_MISMATCH:stubtool" >&2
		exit 1
	fi
	id=$(printf '%s' "$*" | grep -Eo '[0-9a-f]{40}' | head -n 1)
	prev=
	for a in "$@"; do
		case $prev in -t | --tag) printf '%s\t%s\n' "$a" "$id" >>"$STUB_STATE" ;; esac
		case $a in --tag=*) printf '%s\t%s\n' "${a#--tag=}" "$id" >>"$STUB_STATE" ;; esac
		prev=$a
	done
	exit 0
	;;
run | "container run")
	if [ "${STUB_RUN_SLEEP:-0}" = 1 ]; then
		# Signal probe: record pid, record a received TERM, then exit.
		echo $$ >"$STUB_SIG_PID"
		trap 'echo TERM >"$STUB_SIG_GOT"; kill "$cpid" 2>/dev/null; exit 143' TERM
		sleep 60 &
		cpid=$!
		echo "$cpid" >"$STUB_SIG_CHILD"
		wait "$cpid"
		exit 0
	fi
	exit "${STUB_RUN_EXIT:-0}"
	;;
inspect | "image inspect" | "image exists" | "image ls" | "image list" | images)
	[ -n "$ref" ] || exit 1
	lab=$(label_of "$ref")
	case $sub in
	images | "image ls" | "image list")
		[ -z "$lab" ] || { [ "$fmt" = 1 ] && echo "$lab" || echo stubimageid; }
		exit 0
		;;
	esac
	[ -n "$lab" ] || exit 1
	if [ "$fmt" = 1 ]; then echo "$lab"; else printf '[{"Id":"sha256:stub","Config":{"Labels":{"strange.company/toolbox-inputs-id":"%s"}}}]\n' "$lab"; fi
	exit 0
	;;
pull)
	exit 1
	;;
esac
exit 0
