#!/usr/bin/env bash
# サイドカーの E2E（roadmap.md M3）：Node.js の本体（examples/users-node）を YAML のマニフェストとサイドカーで出し、
# Go のライブラリで組み込んだ例（examples/users）と、同じ要求に同じ応答（状態コードと本文）が返ることを確かめる。
# 認証・認可・入力の検証・フィールドの絞り込み・エラーの開示が、ライブラリとサイドカーで食い違わないことを見る。
# 使い方：scripts/e2e-sidecar.sh（node が要る。grpcurl が PATH に無ければ go install する）
set -euo pipefail
cd "$(dirname "$0")/.."

work=$(mktemp -d)
trap 'kill ${pids:-} 2>/dev/null || true; rm -rf "$work"' EXIT
pids=

go build -o "$work/users" ./examples/users/cmd/users
go build -o "$work/api" ./cmd/api
grpcurl=$(command -v grpcurl || true)
if [ -z "$grpcurl" ]; then
  GOBIN="$work" go install github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.3
  grpcurl="$work/grpcurl"
fi

IAM_PUBLIC_URL= "$work/users" serve >"$work/go.log" 2>&1 &
pids="$pids $!"
PORT=3100 API_SIDECAR_UPSTREAM_TOKEN=e2e-secret node examples/users-node/server.mjs >"$work/node.log" 2>&1 &
pids="$pids $!"
IAM_PUBLIC_URL= API_SIDECAR_UPSTREAM_TOKEN=e2e-secret "$work/api" -manifest examples/users/api/manifest.yaml sidecar -dev \
  -upstream http://127.0.0.1:3100 \
  -listen internal=127.0.0.1:8190,partner=127.0.0.1:8191,public=127.0.0.1:8192 \
  -grpc internal=127.0.0.1:9190 >"$work/sidecar.log" 2>&1 &
pids="$pids $!"

up() { curl -s -o /dev/null "$1"; }
for _ in $(seq 100); do
  if up http://127.0.0.1:8090/users && up http://127.0.0.1:8092/users && up http://127.0.0.1:8190/users &&
    up http://127.0.0.1:8192/users && grep -qs "users-node ready" "$work/node.log" && grep -qs "sidecar: ready" "$work/sidecar.log"; then
    ready=1; break
  fi
  sleep 0.1
done
if [ -z "${ready:-}" ]; then
  echo "FAIL: the servers did not start"; cat "$work"/*.log; exit 1
fi

# 同じ要求の並び（状態が変わるので順に）を、ライブラリ（8090〜）とサイドカー（8190〜）に送る。
requests=(
  "internal POST /users dev-internal {\"name\":\"Ada\",\"email\":\"ada@example.com\",\"note\":\"vip\"}"
  "internal POST /users dev-internal {\"name\":\"Bob\",\"email\":\"bob@example.com\"}"
  "internal GET /users/usr_1 dev-internal"
  "partner GET /users/usr_1 dev-partner"
  "public GET /users/usr_1 dev-user"
  "partner GET /users?limit=1 dev-partner"
  "internal GET /users dev-internal"
  "public GET /users dev-user"
  "partner GET /users/usr_9 dev-partner"
  "public GET /users/usr_9 dev-user"
  "internal GET /users/usr_1 -"
  "internal GET /users/usr_1 dev-user"
  "public GET /users/usr_1 nope"
  "partner POST /users dev-partner {\"name\":\"Eve\",\"email\":\"eve@example.com\"}"
  "internal POST /users dev-internal {\"name\":\"Eve\"}"
  "internal POST /users dev-internal {\"name\":\"Eve\",\"email\":\"e@example.com\",\"extra\":1}"
  "internal POST /users dev-internal {\"name\":1,\"email\":\"e@example.com\"}"
  "internal GET /users?limit=abc dev-internal"
  "public GET /users/usr_1?x=1 dev-user"
  "internal DELETE /users/usr_1 dev-internal"
  "internal DELETE /users/usr_1 dev-internal"
  "internal GET /users dev-internal"
)
port() { # base audience
  case $2 in internal) echo $(($1 + 0)) ;; partner) echo $(($1 + 1)) ;; public) echo $(($1 + 2)) ;; esac
}
run() { # base
  local aud method path token body args status
  for r in "${requests[@]}"; do
    read -r aud method path token body <<<"$r"
    args=(-s -X "$method" -o "$work/body" -w '%{http_code}')
    [ "$token" != "-" ] && args+=(-H "Authorization: Bearer $token")
    [ -n "${body:-}" ] && args+=(-H 'Content-Type: application/json' -d "$body")
    status=$(curl "${args[@]}" "http://127.0.0.1:$(port "$1" "$aud")$path")
    echo "$aud $method $path $token: $status $(tr -d '\n' <"$work/body")"
  done
}
run 8090 >"$work/go.out"
run 8190 >"$work/sidecar.out"
grpc() { # port
  "$grpcurl" -plaintext -import-path examples/users/api/proto -proto internal.proto \
    -H "authorization: Bearer dev-internal" -d '{"id":"usr_2"}' "127.0.0.1:$1" users.internal.Users/Get 2>&1 || true
}
echo "grpc Get usr_2: $(grpc 9090)" >>"$work/go.out"
echo "grpc Get usr_2: $(grpc 9190)" >>"$work/sidecar.out"

if ! diff -u "$work/go.out" "$work/sidecar.out"; then
  echo "FAIL: the sidecar answered differently from the library"; exit 1
fi
cat "$work/sidecar.out"
# 見せないフィールドが出ていないこと（diff だけでは、両方とも漏れていても通るので）。
grep -q '^partner GET /users/usr_1 dev-partner: 200 {"email":"ada@example.com","id":"usr_1","name":"Ada"}$' "$work/sidecar.out" ||
  { echo "FAIL: partner projection"; exit 1; }
grep -q '^public GET /users/usr_1 dev-user: 200 {"id":"usr_1","name":"Ada"}$' "$work/sidecar.out" ||
  { echo "FAIL: public projection"; exit 1; }
echo "ok: the sidecar with a Node.js upstream matches the Go library"
