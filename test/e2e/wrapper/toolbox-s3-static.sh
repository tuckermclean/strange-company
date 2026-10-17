#!/bin/sh
# F03 S3 and S2-review static checks, no engine, no network: toolbox/image.lock (plan 02 9.3), the image scan
# configuration (02-AC-9, S2 review N1), shellrc (14.6), `make toolbox` runs alone (S2 review N4).
. "$(dirname "$0")/lib.sh"
echo "toolbox-s3-static (02-AC-9, plan 02 9.3, 14.6, S2 review N1/N4)"
cd "$SC_ROOT" || exit 1

# --- toolbox/image.lock: flat, sed-readable, empty digests until the first release (plan 02 9.3)
if [ -f toolbox/image.lock ]; then
	ok "toolbox/image.lock exists"
	L=$(cat toolbox/image.lock)
	expect_match "image.lock kind is ToolboxImageLock" "^kind: ToolboxImageLock\$" "$L"
	expect_match "image.lock has apiVersion strange.company/v1alpha1" "^apiVersion: strange.company/v1alpha1\$" "$L"
	bad_lines=$(printf '%s\n' "$L" | grep -Ev '^([A-Za-z][A-Za-z0-9]*:( .*)?|#.*|)$' || true)
	expect_eq "image.lock is flat: every line is key: value, a comment or blank" "" "$bad_lines"
	expect_match "image.lock names toolboxImage on ghcr.io" "^toolboxImage: ghcr\.io/" "$L"
	expect_match "image.lock names ciImage on ghcr.io" "^ciImage: ghcr\.io/" "$L"
	expect_match "image.lock has an empty toolboxDigest until the first release" "^toolboxDigest:[[:space:]]*\$" "$L"
	expect_match "image.lock has an empty ciDigest until the first release" "^ciDigest:[[:space:]]*\$" "$L"
	expect_match "image.lock has an inputsId key" "^inputsId:" "$L"
else
	bad "toolbox/image.lock exists"
fi

# --- image scan configuration (S2 review N1): the config is passed explicitly, not shipped inside the image
CF=$(cat toolbox/Containerfile)
copies=$(grep -E '^[[:space:]]*(COPY|ADD)[[:space:]].*\.gitleaks\.toml' toolbox/Containerfile || true)
expect_eq "the Containerfile does not COPY/ADD a .gitleaks.toml (02-AC-9 scans the exported tree with an explicit --config)" "" "$copies"
has() { grep -Fq -- "$2" toolbox/gitleaks-image.toml && ok "$1" || bad "$1" "[$2] not found in toolbox/gitleaks-image.toml"; }
hasnot() { grep -Fq -- "$2" toolbox/gitleaks-image.toml && bad "$1" "[$2] still in toolbox/gitleaks-image.toml" || ok "$1"; }
has "the botocore allowlist is narrowed to service-2 and examples-1 json files" 'botocore/data/.+/(service-2|examples-1)\.json'
hasnot "the botocore allowlist no longer covers all of botocore/data" '(examples|botocore/data)/'
hasnot "the community.general allowlist no longer covers all plugins and docs" 'community/general/(plugins|docs)/'
expect_match "the shell rc file for ./bootstrap shell is installed in the image" "strange-company/shellrc" "$CF"

# --- make toolbox must be run alone (S2 review N4)
RC=0
OUT=$(env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES make -n toolbox lint 2>&1) || RC=$?
[ "$RC" -ne 0 ] && ok "make toolbox lint (other goals together) fails" || bad "make toolbox lint (other goals together) fails" "exit 0"
expect_match "make toolbox with other goals says it must be run alone" "toolbox must be run alone" "$OUT"
RC=0
OUT=$(env -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES SC_IN_CONTAINER=1 make -n toolbox 2>&1) || RC=$?
[ "$RC" -ne 0 ] && ok "make toolbox inside the container (SC_IN_CONTAINER=1) fails" || bad "make toolbox inside the container (SC_IN_CONTAINER=1) fails" "exit 0"
expect_match "make toolbox inside the container says it runs on the host, alone" "toolbox.*(host|alone|container)" "$OUT"
RC=0
env -u SC_IN_CONTAINER -u MAKEFLAGS -u MAKELEVEL -u MAKEOVERRIDES make -n toolbox >/dev/null 2>&1 || RC=$?
expect_eq "make -n toolbox alone still succeeds" 0 "$RC"

finish
