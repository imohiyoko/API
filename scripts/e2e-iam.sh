#!/usr/bin/env bash
# iam を起動した E2E（roadmap.md M2）：サービスアカウントが client credentials で取ったトークン（RFC 8707）で例のサーバーを呼ぶ。
#   - 認証（イントロスペクション）、スコープによる判定の許可・拒否
#   - 口ごとのリソース（aud）で、ほかの口に向けたトークンを拒む
#   - 管理 API 向けの API キーは受けない
# 使い方：scripts/e2e-iam.sh
#   IAM_SRC に iam のソースのディレクトリを渡せばそれを使う。無ければ IAM_REF（既定は下の固定のコミット）を clone する。
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(pwd)

IAM_REF=${IAM_REF:-ccbf26b4f767c39ccdcd20c34bdd104ea83a0c1c}
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
features: [service_accounts, resource_indicators]
YAML

cd "$work"
./iam keys generate -out keys.json >/dev/null
./iam migrate -config iam.yaml >/dev/null
for f in service_accounts resource_indicators; do ./iam features enable -config iam.yaml "$f" >/dev/null 2>&1; done
owner=$(./iam service-account create -config iam.yaml -name e2e-owner -role realm:owner 2>/dev/null | grep '^sac_')
./iam serve -config iam.yaml >iam.log 2>&1 &
iam_pid=$!
wait_for() { # url
  for _ in $(seq 100); do curl -sf "$1" >/dev/null && return 0; sleep 0.1; done
  echo "FAIL: $1 did not come up"; cat iam.log users.log 2>/dev/null || true; exit 1
}
wait_for "$admin/healthz"

json() { python3 -c "import json,sys; print(json.load(sys.stdin)$1)"; }
adm() { # path body
  curl -sSf -H "Authorization: Bearer $owner" -H 'Content-Type: application/json' "$admin$1" -d "$2"
}
service_account() { # name -> "id secret"
  local id secret
  id=$(adm /v1/admin/service-accounts "{\"name\":\"$1\"}" | json '["id"]')
  secret=$(adm "/v1/admin/service-accounts/$id/credentials" '{"type":"client_secret"}' | json '["secret"]')
  echo "$id $secret"
}
token() { # id secret resource scope
  curl -sSf -u "$1:$2" "$admin/v1/admin/token" -d grant_type=client_credentials -d "resource=$3" -d "scope=$4" | json '["access_token"]'
}

# イントロスペクションを呼ぶ連携先（このリソースサーバー）。
client=$(adm /v1/admin/clients '{"name":"users-api","redirect_uris":["https://users.example/cb"],"token_endpoint_auth_method":"client_secret"}')
read -r internal_id internal_secret < <(service_account internal-caller)
read -r public_id public_secret < <(service_account public-caller)
adm /v1/admin/oauth/resources "{\"uri\":\"https://users.internal\",\"name\":\"users internal\",\"scopes\":[\"users:read\",\"users:list\",\"users:create\",\"users:delete\"],\"service_accounts\":[\"$internal_id\"]}" >/dev/null
adm /v1/admin/oauth/resources "{\"uri\":\"https://users.example\",\"name\":\"users public\",\"scopes\":[\"users:read\"],\"service_accounts\":[\"$public_id\"]}" >/dev/null
internal_all=$(token "$internal_id" "$internal_secret" https://users.internal "users:read users:list users:create users:delete")
internal_read=$(token "$internal_id" "$internal_secret" https://users.internal "users:read")
public_read=$(token "$public_id" "$public_secret" https://users.example "users:read")

IAM_PUBLIC_URL=$public IAM_ADMIN_URL=$admin \
  IAM_CLIENT_ID=$(json '["client_id"]' <<<"$client") IAM_CLIENT_SECRET=$(json '["client_secret"]' <<<"$client") \
  IAM_ADMIN_TOKEN=$owner IAM_RESOURCE_INTERNAL=https://users.internal IAM_RESOURCE_PUBLIC=https://users.example \
  ./users serve >users.log 2>&1 &
api_pid=$!
for _ in $(seq 100); do grep -qs "internal gRPC on" users.log && break; kill -0 "$api_pid" 2>/dev/null || break; sleep 0.1; done
grep -q "internal gRPC on" users.log || { echo "FAIL: the server did not start"; cat users.log; exit 1; }

expect() { # name want-status method url token [body]
  local got args=(-s -o "$work/body" -w '%{http_code}' -X "$3" -H "Authorization: Bearer $5" -H 'Content-Type: application/json')
  if [ $# -ge 6 ]; then args+=(-d "$6"); fi
  got=$(curl "${args[@]}" "$4")
  if [ "$got" != "$2" ]; then echo "FAIL: $1: want $2, got $got: $(cat "$work/body")"; exit 1; fi
  echo "ok: $1 ($got)"
}
internal=http://127.0.0.1:8090
pub=http://127.0.0.1:8092
expect "create with the internal token" 200 POST "$internal/users" "$internal_all" '{"name":"Ada","email":"ada@example.com"}'
grep -q '"id":"usr_1"' "$work/body" || { echo "FAIL: create: $(cat "$work/body")"; exit 1; }
expect "get with users:read" 200 GET "$internal/users/usr_1" "$internal_read"
expect "delete without users:delete" 403 DELETE "$internal/users/usr_1" "$internal_read"
grep -q scope_missing "$work/body" || { echo "FAIL: reason: $(cat "$work/body")"; exit 1; }
expect "public token on internal" 401 GET "$internal/users/usr_1" "$public_read"
expect "internal token on public" 401 GET "$pub/users/usr_1" "$internal_all"
expect "get on public" 200 GET "$pub/users/usr_1" "$public_read"
expect "admin API key is not accepted" 401 GET "$internal/users/usr_1" "$owner"
expect "unknown token" 401 GET "$internal/users/usr_1" "sat_nope.nope"
expect "delete with users:delete" 200 DELETE "$internal/users/usr_1" "$internal_all"
cd "$root"
