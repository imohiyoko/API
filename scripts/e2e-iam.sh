#!/usr/bin/env bash
# iam を起動した E2E（roadmap.md M2）：サービスアカウントが client credentials で取ったトークン（RFC 8707）で例のサーバーを呼ぶ。
#   - 認証（イントロスペクション）、iam の authz:check（ロールの付与）とトークンのスコープによる判定の許可・拒否
#   - 口ごとのリソース（aud）で、ほかの口に向けたトークンを拒む
#   - Org に属するサービスアカウントは partner の口を呼べ、属さないものは呼べない
#   - 管理 API 向けの API キーは受けない
# 使い方：scripts/e2e-iam.sh
#   IAM_SRC に iam のソースのディレクトリを渡せばそれを使う。無ければ IAM_REF（既定は下の固定のコミット）を clone する。
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)

IAM_REF=${IAM_REF:-516228fe81348753893673d979b42867c222725a}
work=$(mktemp -d)
trap 'kill "${iam_pid:-}" "${api_pid:-}" 2>/dev/null || true; rm -rf "$work"' EXIT

if [ -z "${IAM_SRC:-}" ]; then
  git init -q "$work/iam-src"
  git -C "$work/iam-src" fetch -q --depth 1 https://github.com/imohiyoko/iam.git "$IAM_REF"
  git -C "$work/iam-src" checkout -q FETCH_HEAD
  IAM_SRC="$work/iam-src"
fi
(cd "$IAM_SRC" && go build -o "$work/iam" ./cmd/iam)
go build -o "$work/users" ./examples/users/cmd/users
go build -o "$work/api" ./cmd/api

public=http://localhost:18080
admin=http://127.0.0.1:18081
cat >"$work/iam.yaml" <<YAML
dev: true
server:
  public_addr: "localhost:18080"
  admin_addr: "127.0.0.1:18081"
  public_url: "$public"
storage: { driver: sqlite, path: "$work/iam.db", auto_migrate: "true" }
ephemeral: { driver: memory }
keystore: { driver: file, path: "$work/keys.json" }
event_sink: { driver: stdout }
notifier: { driver: dev }
features: [service_accounts, resource_indicators, org]
YAML

cd "$work"
./iam keys generate -out keys.json >/dev/null
./iam migrate -config iam.yaml >/dev/null
for f in service_accounts resource_indicators org; do ./iam features enable -config iam.yaml "$f" >/dev/null 2>&1; done
owner=$(./iam service-account create -config iam.yaml -name e2e-owner -role realm:owner 2>/dev/null | grep '^sac_')
./iam serve -config iam.yaml >iam.log 2>&1 &
iam_pid=$!
wait_for() { # url
  for _ in $(seq 100); do curl -sf "$1" >/dev/null && return 0; sleep 0.1; done
  echo "FAIL: $1 did not come up"; cat iam.log users.log 2>/dev/null || true; exit 1
}
wait_for "$admin/healthz"

json() { python3 -c "import json,sys; s=sys.stdin.read()
try: print(json.loads(s)$1)
except Exception: sys.exit(\"bad response: \"+s)"; }
adm() { # path body
  curl -sS --fail-with-body -H "Authorization: Bearer $owner" -H 'Content-Type: application/json' "$admin$1" -d "$2"
}
service_account() { # name [org] -> "id secret"
  local id secret body="{\"name\":\"$1\"}"
  if [ -n "${2:-}" ]; then body="{\"name\":\"$1\",\"org_id\":\"$2\"}"; fi
  id=$(adm /v1/admin/service-accounts "$body" | json '["id"]')
  secret=$(adm "/v1/admin/service-accounts/$id/credentials" '{"type":"client_secret"}' | json '["secret"]')
  echo "$id $secret"
}
token() { # id secret resource scope
  curl -sSf -u "$1:$2" "$admin/v1/admin/token" -d grant_type=client_credentials -d "resource=$3" -d "scope=$4" | json '["access_token"]'
}

grant() { # service-account-id role-id
  adm /v1/admin/role-bindings "{\"role\":\"$2\",\"principal_type\":\"service_account\",\"principal_id\":\"$1\"}" >/dev/null
}

# イントロスペクションを呼ぶ連携先（このリソースサーバー）。
client=$(adm /v1/admin/clients '{"name":"users-api","redirect_uris":["https://users.example/cb"],"token_endpoint_auth_method":"client_secret"}')
client_id=$(json '["client_id"]' <<<"$client")
# 提携先の Org（owner のユーザーが要る）。
owner_user=$(adm /v1/admin/users '{"email":"owner@partner.example"}' | json '["user_id"]')
org=$(adm /v1/admin/orgs "{\"name\":\"partner\",\"owner_id\":\"$owner_user\"}" | json '["id"]')

# 操作の権限は、マニフェストから書き出した iam の権限のカタログ（iam の ADR 0121）に載せ、ロールで付与する（authz:check。iam#152）。
"$work/api" -manifest "$root/examples/users/api/manifest.yaml" iam-catalog -prefix users-api >catalog.json
adm /v1/admin/permission-catalogs "$(cat catalog.json)" >/dev/null
# 判定を問い合わせるサービスアカウントには、このカタログの authz:check だけを渡す（realm 全体の判定はさせない）。
checker_role=$(adm /v1/admin/roles '{"name":"users-api-checker","permissions":["authz:check"]}' | json '["id"]')
read -r checker_id _ < <(service_account users-api-checker)
adm /v1/admin/role-bindings "{\"role\":\"$checker_role\",\"principal_type\":\"service_account\",\"principal_id\":\"$checker_id\",\"resource_type\":\"permission_catalog\",\"resource_id\":\"users-api\"}" >/dev/null
checker_key=$(adm "/v1/admin/service-accounts/$checker_id/credentials" '{"type":"api_key"}' | json '["secret"]')
editor=$(adm /v1/admin/roles '{"name":"users-editor","permissions":["users-api.users:read","users-api.users:list","users-api.users:create","users-api.users:delete"]}' | json '["id"]')
reader=$(adm /v1/admin/roles '{"name":"users-reader","permissions":["users-api.users:read","users-api.users:list"]}' | json '["id"]')
read -r internal_id internal_secret < <(service_account internal-caller)
read -r reader_id reader_secret < <(service_account internal-reader)
read -r public_id public_secret < <(service_account public-caller)
read -r partner_id partner_secret < <(service_account partner-caller "$org")
grant "$internal_id" "$editor"
grant "$reader_id" "$reader"
grant "$public_id" "$reader"
grant "$partner_id" "$reader"
all='users:read users:list users:create users:delete'
scopes='["users:read","users:list","users:create","users:delete"]'
# introspection_clients でこのリソースサーバーだけが確かめられるようにする（iam の ADR 0120）。
resource() { # uri name service-accounts-json
  adm /v1/admin/oauth/resources "{\"uri\":\"$1\",\"name\":\"$2\",\"scopes\":$scopes,\"service_accounts\":$3,\"introspection_clients\":[\"$client_id\"]}" >/dev/null
}
resource https://users.internal "users internal" "[\"$internal_id\",\"$reader_id\"]"
resource https://users.partner "users partner" "[\"$partner_id\",\"$internal_id\"]"
resource https://users.example "users public" "[\"$public_id\"]"
internal_all=$(token "$internal_id" "$internal_secret" https://users.internal "$all")
internal_read=$(token "$internal_id" "$internal_secret" https://users.internal "users:read")
reader_all=$(token "$reader_id" "$reader_secret" https://users.internal "$all")
public_read=$(token "$public_id" "$public_secret" https://users.example "users:read")
partner_read=$(token "$partner_id" "$partner_secret" https://users.partner "users:read")
no_org_partner=$(token "$internal_id" "$internal_secret" https://users.partner "users:read")

IAM_PUBLIC_URL=$public IAM_ADMIN_URL=$admin \
  IAM_CLIENT_ID=$client_id IAM_CLIENT_SECRET=$(json '["client_secret"]' <<<"$client") \
  IAM_ADMIN_TOKEN=$checker_key IAM_PERMISSION_CATALOG=users-api IAM_RESOURCE_INTERNAL=https://users.internal IAM_RESOURCE_PARTNER=https://users.partner IAM_RESOURCE_PUBLIC=https://users.example \
  ./users serve >users.log 2>&1 &
api_pid=$!
# HTTP の口は別の goroutine で待ち受けるので、ログではなく3つの口すべてに繋がるまで待つ。
up() { curl -s -o /dev/null "http://127.0.0.1:8090/users" && curl -s -o /dev/null "http://127.0.0.1:8091/users" && curl -s -o /dev/null "http://127.0.0.1:8092/users"; }
ready=
for _ in $(seq 100); do
  if up; then ready=1; break; fi
  kill -0 "$api_pid" 2>/dev/null || break
  sleep 0.1
done
[ -n "$ready" ] || { echo "FAIL: the server did not start"; cat users.log; exit 1; }

expect() { # name want-status method url token [body]
  local got args=(-s -o "$work/body" -w '%{http_code}' -X "$3" -H "Authorization: Bearer $5" -H 'Content-Type: application/json')
  if [ $# -ge 6 ]; then args+=(-d "$6"); fi
  got=$(curl "${args[@]}" "$4")
  if [ "$got" != "$2" ]; then echo "FAIL: $1: want $2, got $got: $(cat "$work/body")"; exit 1; fi
  echo "ok: $1 ($got)"
}
internal=http://127.0.0.1:8090
partner=http://127.0.0.1:8091
pub=http://127.0.0.1:8092
expect "create with the internal token" 200 POST "$internal/users" "$internal_all" '{"name":"Ada","email":"ada@example.com"}'
grep -q '"id":"usr_1"' "$work/body" || { echo "FAIL: create: $(cat "$work/body")"; exit 1; }
expect "get with users:read" 200 GET "$internal/users/usr_1" "$internal_read"
expect "delete without users:delete" 403 DELETE "$internal/users/usr_1" "$internal_read"
grep -q scope_missing "$work/body" || { echo "FAIL: reason: $(cat "$work/body")"; exit 1; }
expect "delete without the role" 403 DELETE "$internal/users/usr_1" "$reader_all"
if grep -q scope_missing "$work/body"; then echo "FAIL: want iam's denial: $(cat "$work/body")"; exit 1; fi
expect "get on partner from the Org" 200 GET "$partner/users/usr_1" "$partner_read"
grep -q '"email"' "$work/body" && ! grep -q '"note"' "$work/body" || { echo "FAIL: partner projection: $(cat "$work/body")"; exit 1; }
expect "partner without an Org" 403 GET "$partner/users/usr_1" "$no_org_partner"
grep -q principal_not_allowed "$work/body" || { echo "FAIL: reason: $(cat "$work/body")"; exit 1; }
expect "partner token on internal" 401 GET "$internal/users/usr_1" "$partner_read"
expect "internal token on partner" 401 GET "$partner/users/usr_1" "$internal_read"
expect "public token on internal" 401 GET "$internal/users/usr_1" "$public_read"
expect "internal token on public" 401 GET "$pub/users/usr_1" "$internal_all"
expect "get on public" 200 GET "$pub/users/usr_1" "$public_read"
expect "admin API key is not accepted" 401 GET "$internal/users/usr_1" "$owner"
expect "unknown token" 401 GET "$internal/users/usr_1" "sat_nope.nope"
expect "delete with users:delete" 200 DELETE "$internal/users/usr_1" "$internal_all"
cd "$root"
