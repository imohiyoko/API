# API

1つの操作（エンドポイント）を1回だけ定義すると、internal / partner / public などの公開範囲と、REST / GraphQL / gRPC / WebSocket / Webhook などのプロトコルごとの API が自動で作られるライブラリ（設計段階）。

- 設計: [docs/design.md](docs/design.md)
- ロードマップ: [docs/roadmap.md](docs/roadmap.md)
- 設計判断の記録（ADR）: [docs/adr/](docs/adr/README.md)

認証・認可は [imohiyoko/iam](https://github.com/imohiyoko/iam) を使う。
