#!/usr/bin/env bash
# gRPC の E2E（roadmap.md M2）：生成した .proto だけを使い、grpcurl から例のサーバーを呼ぶ。
# 使い方：scripts/e2e-grpc.sh（grpcurl が PATH に無ければ go install する）
set -euo pipefail
cd "$(dirname "$0")/.."

work=$(mktemp -d)
trap 'kill "${pid:-}" 2>/dev/null || true; rm -rf "$work"' EXIT

go build -o "$work/users" ./examples/users/cmd/users
grpcurl=$(command -v grpcurl || true)
if [ -z "$grpcurl" ]; then
  GOBIN="$work" go install github.com/fullstorydev/grpcurl/cmd/grpcurl@v1.9.3
  grpcurl="$work/grpcurl"
fi

# 開発用のトークンを使うため、iam の設定は消して起動する。
IAM_PUBLIC_URL= "$work/users" serve >"$work/serve.log" 2>&1 &
pid=$!
ready=
for _ in $(seq 100); do
  if grep -q "internal gRPC on" "$work/serve.log"; then ready=1; break; fi
  # 起動に失敗した（ポートが使われているなど）なら、ほかのサーバーを呼ばないように止める。
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.1
done
if [ -z "$ready" ]; then
  echo "FAIL: the server did not start"; cat "$work/serve.log"; exit 1
fi

call() { # token method json
  "$grpcurl" -plaintext -import-path examples/users/api/proto -proto internal.proto \
    -H "authorization: Bearer $1" -d "$3" 127.0.0.1:9090 "users.internal.Users/$2" 2>&1 || true
}
expect() { # name output pattern
  if ! grep -q -- "$3" <<<"$2"; then
    echo "FAIL: $1: want $3, got:"; echo "$2"; exit 1
  fi
  echo "ok: $1"
}

out=$(call dev-internal Create '{"name":"Ada","email":"ada@example.com"}')
expect create "$out" '"id": "usr_1"'
out=$(call dev-internal Get '{"id":"usr_1"}')
expect get "$out" '"email": "ada@example.com"'
out=$(call dev-internal List '{}')
expect list "$out" '"name": "Ada"'
out=$(call dev-user Get '{"id":"usr_1"}')
expect "user token on internal" "$out" 'PermissionDenied'
out=$(call "" Get '{"id":"usr_1"}')
expect "no token" "$out" 'Unauthenticated'
out=$(call dev-internal Get '{"id":"usr_9"}')
expect "not found" "$out" 'NotFound'
out=$(call dev-internal Delete '{"id":"usr_1"}')
expect delete "$out" '{}'
