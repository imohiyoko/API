# API

1つの操作（エンドポイント）を1回だけ定義すると、internal / partner / public などの公開範囲と、REST / GraphQL / gRPC / WebSocket / Webhook などのプロトコルごとの API が自動で作られるライブラリ（設計段階）。

- 設計: [docs/design.md](docs/design.md)
- ロードマップ: [docs/roadmap.md](docs/roadmap.md)
- 設計判断の記録（ADR）: [docs/adr/](docs/adr/README.md)

認証・認可は [imohiyoko/iam](https://github.com/imohiyoko/iam) を使う。

## 使い方

```go
var getUser = define.Query[GetUserIn, User]("users.get",
	define.Authz("users:read", define.Resource("user", "id")),
	define.Expose(define.Internal, define.REST, define.GRPC),
	define.Expose(define.Public, define.REST),
).Handle(func(ctx context.Context, in GetUserIn) (User, error) { /* ... */ })

reg := define.NewRegistry(define.WithName("users")).Add(getUser)
srv, err := runtime.Build(reg, runtime.Config{Authenticator: iamClient, Authorizer: iamClient})
// 公開範囲・プロトコルごとに別の listener
go func() { log.Fatal(http.ListenAndServe(":8092", srv.Handler(define.Public))) }()
gs, err := srv.GRPCServer(define.Internal) // 同じ定義の gRPC（users.internal.Users/Get）
lis, err := net.Listen("tcp", "127.0.0.1:9090")
log.Fatal(gs.Serve(lis))
```

例は [examples/users](examples/users)。生成した契約ファイルは [examples/users/api](examples/users/api)。

```sh
go test ./...
go run ./examples/users/cmd/users gen -out examples/users/api -title Users
go run ./examples/users/cmd/users serve   # 開発用のトークン dev-internal・dev-partner・dev-user
scripts/e2e-grpc.sh                       # 生成した .proto で grpcurl から呼ぶ E2E
```

Go 1.26 以上が要る。
