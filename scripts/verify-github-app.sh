#!/usr/bin/env bash
# Verify a GitHub App ID and private key actually belong together, and that
# the App is installed where you think it is.
#
#   scripts/verify-github-app.sh <app-id> <path-to.pem> [owner/repo ...]
#
# The key is read locally and never leaves this machine: the only thing sent
# to GitHub is a short-lived signed assertion, which is what the control plane
# sends too.
set -euo pipefail

[ $# -ge 2 ] || { sed -n '2,9p' "$0" | sed 's/^# \?//' >&2; exit 2; }

app_id=$1; pem=$2; shift 2
api=${GITHUB_API_URL:-https://api.github.com}

[ -r "$pem" ] || { echo "cannot read $pem" >&2; exit 2; }
grep -q 'BEGIN ENCRYPTED PRIVATE KEY' "$pem" && {
  echo "that key is passphrase-encrypted; the control plane cannot read it." >&2
  echo "generate a fresh one -- GitHub hands out unencrypted PKCS#1." >&2
  exit 2; }

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

now=$(date +%s)
header=$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)
# iat backdated a minute for clock skew, exp well inside GitHub's 10-minute cap
payload=$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' "$((now-60))" "$((now+540))" "$app_id" | b64url)
signature=$(printf '%s.%s' "$header" "$payload" \
  | openssl dgst -sha256 -sign "$pem" -binary | b64url)
jwt="$header.$payload.$signature"

hit() { curl -sS -w '\n%{http_code}' -H "Authorization: Bearer $jwt" \
        -H 'Accept: application/vnd.github+json' \
        -H 'X-GitHub-Api-Version: 2022-11-28' "$api$1"; }

out=$(hit /app); code=${out##*$'\n'}; body=${out%$'\n'*}
if [ "$code" != 200 ]; then
  echo "FAIL  the App ID and this key do not go together ($code)"
  echo "$body" | sed 's/^/      /'
  exit 1
fi

jqr() { command -v jq >/dev/null && echo "$body" | jq -r "$1" || echo '?'; }
echo "OK    app id $app_id is $(jqr .slug) (\"$(jqr .name)\"), owned by $(jqr .owner.login)"
echo "      permissions: $(jqr '.permissions|to_entries|map("\(.key)=\(.value)")|join(", ")')"

# Workflows write is the one permission that lets a credential rewrite the
# checks that gate the work. The runner refuses such commits; the App should
# not carry the permission either unless day-0 import needs it.
[ "$(jqr '.permissions.workflows // "none"')" = write ] &&
  echo "WARN  this App can write .github/workflows -- only day-0 import should."

for repo in "$@"; do
  out=$(hit "/repos/$repo/installation"); code=${out##*$'\n'}; body=${out%$'\n'*}
  case $code in
    200) echo "OK    installed on $repo (installation $(jqr .id))" ;;
    404) echo "FAIL  not installed on $repo"; rc=1 ;;
    *)   echo "FAIL  $repo: $code $(jqr .message)"; rc=1 ;;
  esac
done
exit "${rc:-0}"
