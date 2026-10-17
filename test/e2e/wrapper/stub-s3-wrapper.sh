#!/bin/sh
# F03 S3 wrapper checks with the stub engine (plan 02 sections 7.1, 13, 14.2 steps 2-3 and 8, 14.3, 14.5, 14.6, 15):
# default image (02-AC-7, 02-AC-13 literal commands), env file read without evaluation (02-AC-11), no credential in the
# engine arguments (02-AC-6, 02-R-3), mounts, black-start directory, SELinux options (02-R-4), shell, dispatch.
. "$(dirname "$0")/lib-s3.sh"
echo "stub-s3-wrapper (02-AC-5, 02-AC-6, 02-AC-7, 02-AC-11, 02-AC-13, 02-R-3, 02-R-4)"

fx=$TMP/repo
mkrepo "$fx"
REPO=$(cd -P "$fx" && pwd -P)
HOME_=$TMP/home
IMG=${SC_TEST_IMG:-}  # validation seam for the harness itself only; the criteria run without --image
id=$(ref_inputs_id "$fx")
ttag=localhost/strange-company-toolbox:in-$(id12 "$id")
mkdir -p "$HOME_/.ssh" "$HOME_/keys"

# --- default image is toolbox (02-AC-7, 02-AC-13 use ./bootstrap exec $IMG -- ... with no --image)
reset_log
STUB_RUN_EXIT=7 wrapx "$fx" "" exec $IMG -- sh -c 'exit 7'
expect_eq "02-AC-7 exec $IMG -- sh -c 'exit 7' without --image returns 7" 7 "$RC"
L=$(run_line)
expect_match "02 exec without --image runs the toolbox image" "$ttag( |\$)" "$L"
expect_match "02 exec without --image builds the toolbox target" "--target[ =]toolbox( |\$)" "$(grep -E '^(build|image build|buildx build) ' "$STUB_LOG")"
expect_match "02-AC-7 the command words reach the container after the image" "$ttag( exec)?( --)? sh -c exit 7\$" "$L"
expect_match "02-AC-7 stdin stays open (-i) for pipes" " -i( |\$)|-[a-z]*i[a-z]* " "$L"
expect_nomatch "02-AC-7 no TTY is requested when stdin is not a TTY" " (-t|-it|-ti|--tty)( |\$)" "$L"
expect_nomatch "02-AC-13 no engine socket is mounted (docker.sock, podman.sock)" "docker\.sock|podman\.sock" "$L"
reset_log
wrapx "$fx" "" exec $IMG --
expect_eq "02 exec with no command exits 2 (usage)" 2 "$RC"
expect_eq "02 exec with no command runs no container" 0 "$(runs)"
reset_log
wrapx "$fx" "" exec --image ci -- true
expect_match "02 --image ci still selects the ci image" "localhost/strange-company-ci:in-$(id12 "$id")( |\$)" "$(run_line)"

# --- ./bootstrap shell (plan 02 14.6): same container, the image dispatches "shell"
reset_log
wrapx "$fx" "" shell
expect_eq "02 shell exits 0" 0 "$RC"
expect_match "02 shell runs the toolbox image with the argument shell" "$ttag shell\$" "$(run_line)"
reset_log
wrapx "$fx" "" shell --network none
expect_match "02 shell --network none passes the network flag to the engine" "--network[ =]none" "$(run_line)"
expect_match "02 shell --network none still ends with the argument shell" "$ttag shell\$" "$(run_line)"

# --- every other command is passed unchanged to the image, which hands it to sc-bootstrap (plan 02 7.1, 14.5)
for verb in run plan preflight status verify expand destroy report rotate state unlock version; do
	reset_log
	wrapx "$fx" "" "$verb" --flag value
	expect_eq "02 $verb exits 0" 0 "$RC"
	expect_match "02 $verb --flag value is passed unchanged after the image" "$ttag $verb --flag value\$" "$(run_line)"
done
reset_log
STUB_RUN_EXIT=3 wrapx "$fx" "" preflight
expect_eq "02 preflight returns the container exit code (3)" 3 "$RC"

# --- env file: default $REPO/bootstrap.env, read without evaluation (plan 02 14.2 steps 2-3)
C1=CANARY-S3SECRET-7f3a91
C2=CANARY-PASSPHRASE-b2c4d8
C3=CANARY-KEYBODY-55e0aa
printf -- '-----BEGIN OPENSSH PRIVATE KEY-----\n%s\n-----END OPENSSH PRIVATE KEY-----\n' "$C3" >"$HOME_/keys/mykey"
printf 'ssh-ed25519 AAAA%s test\n' "$C3" >"$HOME_/keys/mykey.pub"
chmod 600 "$HOME_/keys/mykey"
chmod 644 "$HOME_/keys/mykey.pub"
cat >"$fx/bootstrap.env" <<EOT
# operator settings
CLUSTER_NAME=demo
S3_SECRET_ACCESS_KEY=$C1
RECOVERY_PASSPHRASE='$C2'
# SSH_PRIVATE_KEY_PATH=$HOME_/keys/commented-out
SSH_PRIVATE_KEY_PATH="~/keys/mykey"
SSH_PUBLIC_KEY_PATH='~/keys/mykey.pub'
EOT
export AWS_SECRET_ACCESS_KEY=CANARY-HOSTENV-AWS-33
reset_log
wrapx "$fx" "" exec $IMG -- true
expect_eq "02-AC-6 exec with a canary env file exits 0" 0 "$RC"
L=$(run_line)
expect_match "02-AC-6 the default env file \$REPO/bootstrap.env is mounted read-only at /run/sc-input/bootstrap.env" "$(mount_re "$REPO/bootstrap.env" /run/sc-input/bootstrap.env ro)" "$L"
expect_match "02 SSH_PRIVATE_KEY_PATH (quotes removed, ~/ is the host \$HOME) is mounted ro at /run/sc-input/ssh/id_key" "$(mount_re "$HOME_/keys/mykey" /run/sc-input/ssh/id_key ro)" "$L"
expect_match "02 SSH_PUBLIC_KEY_PATH is mounted ro at /run/sc-input/ssh/id_key.pub" "$(mount_re "$HOME_/keys/mykey.pub" /run/sc-input/ssh/id_key.pub ro)" "$L"
expect_nomatch "02 a commented-out SSH_PRIVATE_KEY_PATH line assigns nothing" "commented-out" "$L"
LOG=$(log_all)
for c in "$C1" "$C2" "$C3"; do
	expect_absent "02-AC-6 canary $c is not in the engine argument log" "$c" "$LOG"
done
expect_absent "02-AC-6 a host environment secret is not forwarded to the container" "CANARY-HOSTENV-AWS-33" "$LOG"
expect_nomatch "02-R-3 the engine's own --env-file is never used (it would put the file into the container env)" "(^| )--env-file( |=)" "$L"
expect_nomatch "02-R-4 no :z or :Z relabel volume option" ":[^ ]*[zZ]( |,|\$)" "$L"
unset AWS_SECRET_ACCESS_KEY

# --env-file PATH
cp "$fx/bootstrap.env" "$TMP/other.env"
reset_log
wrapx "$fx" "" --env-file "$TMP/other.env" exec $IMG -- true
expect_match "02 --env-file PATH mounts that file at /run/sc-input/bootstrap.env" "$(mount_re "$TMP/other.env" /run/sc-input/bootstrap.env ro)" "$(run_line)"
expect_nomatch "02 --env-file PATH: the default env file is not mounted" "$REPO/bootstrap.env" "$(run_line)"

# absent env file: exec still works, nothing mounted
mv "$fx/bootstrap.env" "$TMP/bootstrap.env.bak"
reset_log
wrapx "$fx" "" exec $IMG -- true
expect_eq "02 exec without an env file exits 0" 0 "$RC"
expect_nomatch "02 no env file: nothing is mounted at /run/sc-input/bootstrap.env" "/run/sc-input/bootstrap\.env" "$(run_line)"
mv "$TMP/bootstrap.env.bak" "$fx/bootstrap.env"

# --- 02-AC-11: the literal path, never evaluated
rm -f "$TMP/pwned" "$TMP/pwned2" "$TMP/pwned3"
cat >"$fx/bootstrap.env" <<EOT
SSH_PRIVATE_KEY_PATH=\$(touch $TMP/pwned)
SSH_PUBLIC_KEY_PATH=\`touch $TMP/pwned2\`
SSH_USE_AGENT=\$(touch $TMP/pwned3)
EOT
reset_log
wrapx "$fx" "" exec $IMG -- true
expect_eq "02-AC-11 a command substitution in the env file does not stop the wrapper (PF-06 reports it later)" 0 "$RC"
if [ -e "$TMP/pwned" ] || [ -e "$TMP/pwned2" ] || [ -e "$TMP/pwned3" ]; then bad "02-AC-11 the env file value was evaluated on the host"; else ok "02-AC-11 nothing was created on the host by the env file values"; fi
expect_nomatch "02-AC-11 the unevaluated literal is not used as a mount source" "touch" "$(run_line)"
# Lines outside ^[A-Z][A-Z0-9_]*= assign nothing (plan 02 14.2 step 3).
cat >"$fx/bootstrap.env" <<EOT
export SSH_PRIVATE_KEY_PATH=$HOME_/keys/mykey
ssh_private_key_path=$HOME_/keys/mykey
 SSH_PRIVATE_KEY_PATH=$HOME_/keys/mykey
EOT
reset_log
wrapx "$fx" "" exec $IMG -- true
expect_nomatch "02 lines that do not match ^[A-Z][A-Z0-9_]*= assign nothing (no key mounted)" "/run/sc-input/ssh/id_key" "$(run_line)"

# --- default SSH key ~/.ssh/id_ed25519(.pub): resolved on the host, mounted only if it exists (plan 02 14.2 step 3)
printf 'CLUSTER_NAME=demo\n' >"$fx/bootstrap.env"
reset_log
wrapx "$fx" "" exec $IMG -- true
expect_nomatch "02 no default key on the host: no key is mounted" "/run/sc-input/ssh/id_key" "$(run_line)"
printf -- '-----BEGIN OPENSSH PRIVATE KEY-----\n%s\n-----END OPENSSH PRIVATE KEY-----\n' "$C3" >"$HOME_/.ssh/id_ed25519"
printf 'ssh-ed25519 AAAA%s test\n' "$C3" >"$HOME_/.ssh/id_ed25519.pub"
chmod 600 "$HOME_/.ssh/id_ed25519"
reset_log
wrapx "$fx" "" exec $IMG -- true
L=$(run_line)
expect_match "02 default key ~/.ssh/id_ed25519 (host \$HOME) is mounted ro at /run/sc-input/ssh/id_key" "$(mount_re "$HOME_/.ssh/id_ed25519" /run/sc-input/ssh/id_key ro)" "$L"
expect_match "02 default public key is mounted ro at /run/sc-input/ssh/id_key.pub" "$(mount_re "$HOME_/.ssh/id_ed25519.pub" /run/sc-input/ssh/id_key.pub ro)" "$L"
expect_nomatch "02 a private key with mode 600 gives no key-mode warning" "[Ww]arn" "$ERR x"
expect_absent "02-AC-6 the key content is not in the engine argument log" "$C3" "$(log_all)"
# PF-06 host side: a key readable by group or others makes the wrapper warn (the run still proceeds).
chmod 644 "$HOME_/.ssh/id_ed25519"
reset_log
wrapx "$fx" "" exec $IMG -- true
expect_eq "02 a group/other-readable key only warns: exit 0" 0 "$RC"
expect_match "02 PF-06 host side: the wrapper warns about the key mode and names the key" "[Ww]arn.*id_ed25519|id_ed25519.*[Ww]arn" "$ERR"
expect_match "02 the key-mode warning says group or others" "(group|others|world)" "$ERR"
expect_absent "02 the key-mode warning does not print the key content" "$C3" "$ERR"
chmod 600 "$HOME_/.ssh/id_ed25519"

# --- ssh agent: mounted only when SSH_USE_AGENT=true and SSH_AUTH_SOCK is set
python3 -I -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$TMP/agent.sock" 2>/dev/null || : >"$TMP/agent.sock"
printf 'SSH_USE_AGENT=true\n' >"$fx/bootstrap.env"
reset_log
wrapx "$fx" "SSH_AUTH_SOCK=$TMP/agent.sock" exec $IMG -- true
expect_match "02 SSH_USE_AGENT=true mounts \$SSH_AUTH_SOCK at /run/sc-input/ssh-agent.sock" "(-v|--volume)[ =]$TMP/agent.sock:/run/sc-input/ssh-agent\.sock(:rw)?( |\$)|--mount[ =][^ ]*src=$TMP/agent.sock,[^ ]*(dst|target)=/run/sc-input/ssh-agent.sock" "$(run_line)"
reset_log
wrapx "$fx" "" exec $IMG -- true
expect_eq "02 SSH_USE_AGENT=true without SSH_AUTH_SOCK: the wrapper still runs" 0 "$RC"
expect_nomatch "02 SSH_USE_AGENT=true without SSH_AUTH_SOCK: no agent mount" "ssh-agent\.sock" "$(run_line)"
printf 'SSH_USE_AGENT=false\n' >"$fx/bootstrap.env"
reset_log
wrapx "$fx" "SSH_AUTH_SOCK=$TMP/agent.sock" exec $IMG -- true
expect_nomatch "02 SSH_USE_AGENT=false: the agent socket is not mounted" "ssh-agent\.sock" "$(run_line)"
printf 'CLUSTER_NAME=demo\n' >"$fx/bootstrap.env"

# --- black-start directory (plan 02 9.1, 14.3)
rm -rf "$HOME_/.strange-company"
reset_log
wrapx "$fx" "" exec $IMG -- true
expect_match "02 \$HOME/.strange-company is mounted rw at /blackstart" "$(mount_re "$HOME_/.strange-company" /blackstart "")" "$(run_line)"
expect_nomatch "02 /blackstart is mounted read-write (no :ro)" "/blackstart:ro" "$(run_line)"
expect_eq "02 the default black-start directory is created with mode 0700" 700 "$(stat -c %a "$HOME_/.strange-company" 2>/dev/null)"
reset_log
wrapx "$fx" "SC_BLACKSTART_DIR=$TMP/bs" exec $IMG -- true
expect_match "02 SC_BLACKSTART_DIR overrides the host directory" "$(mount_re "$TMP/bs" /blackstart "")" "$(run_line)"
expect_eq "02 SC_BLACKSTART_DIR is created with mode 0700" 700 "$(stat -c %a "$TMP/bs" 2>/dev/null)"
mkdir -p "$TMP/bs-open"
chmod 775 "$TMP/bs-open"
reset_log
wrapx "$fx" "SC_BLACKSTART_DIR=$TMP/bs-open" exec $IMG -- true
[ "$RC" -ne 0 ] && ok "02 a group-writable black-start directory is refused" || bad "02 a group-writable black-start directory is refused" "exit 0"
expect_match "02 the refusal names the black-start directory" "blackstart|BLACKSTART|black-start" "$ERR"
expect_eq "02 a refused black-start directory runs no container" 0 "$(runs)"
chmod 757 "$TMP/bs-open"
reset_log
wrapx "$fx" "SC_BLACKSTART_DIR=$TMP/bs-open" exec $IMG -- true
[ "$RC" -ne 0 ] && ok "02 a world-writable black-start directory is refused" || bad "02 a world-writable black-start directory is refused" "exit 0"
chmod 755 "$TMP/bs-open"
reset_log
wrapx "$fx" "SC_BLACKSTART_DIR=$TMP/bs-open" exec $IMG -- true
expect_eq "02 a black-start directory that is not group/world-writable (0755) is accepted" 0 "$RC"

# --- worktrees: the git common dir outside $REPO is mounted at its own path (plan 02 14.3)
git -C "$fx" worktree add -q "$TMP/wt" -b wt 2>/dev/null
reset_log
wrapx "$TMP/wt" "" exec $IMG -- true
WT=$(cd -P "$TMP/wt" && pwd -P)
expect_match "02 worktree: the repository is the worktree" "$(mount_re "$WT" "$WT" "")" "$(run_line)"
expect_match "02 worktree: the common git dir is mounted at the same path" "$(mount_re "$REPO/.git" "$REPO/.git" "")" "$(run_line)"

# --- SELinux options (02-R-4): always with Podman, never :z/:Z
for rl in 0 1; do
	reset_log
	STUB_ROOTLESS=$rl ENGINE=podman wrapx "$fx" "" exec $IMG -- true
	expect_match "02-R-4 podman (rootless=$rl): --security-opt label=disable" "--security-opt[ =]label=disable( |\$)" "$(run_line)"
	expect_nomatch "02-R-4 podman (rootless=$rl): no :z/:Z on any volume" ":[^ ]*[zZ]( |,|\$)" "$(run_line)"
done

# --- the environment contract in the run line (plan 02 7.2, 14.5)
reset_log
STUB_ROOTLESS=1 ENGINE=podman wrapx "$fx" "" exec $IMG -- true
L=$(run_line)
expect_match "02 SC_TOOLBOX_SOURCE=built for a locally built image" "(-e|--env)[ =]SC_TOOLBOX_SOURCE=built( |\$)" "$L"
expect_match "02 SC_ENGINE names the mode" "(-e|--env)[ =]SC_ENGINE=podman-rootless( |\$)" "$L"
expect_match "02 SC_TOOLBOX_IMAGE_REF names the local tag" "(-e|--env)[ =]SC_TOOLBOX_IMAGE_REF=$ttag(@[^ ]*)?( |\$)" "$L"

# --- host root is refused unless SC_ALLOW_ROOT=1 (plan 02 14.2 step 1)
reset_log
wrapx "$fx" "PATH=$ROOTBIN:$STUBBIN:$PATH" exec $IMG -- true
expect_eq "02 running as host root exits 2" 2 "$RC"
expect_match "02 running as host root reports WRAPPER_RUNNING_AS_ROOT" "WRAPPER_RUNNING_AS_ROOT" "$ERR"
expect_eq "02 running as host root runs no container" 0 "$(runs)"
reset_log
wrapx "$fx" "PATH=$ROOTBIN:$STUBBIN:$PATH SC_ALLOW_ROOT=1" exec $IMG -- true
expect_eq "02 SC_ALLOW_ROOT=1 lets host root through" 0 "$RC"

finish
