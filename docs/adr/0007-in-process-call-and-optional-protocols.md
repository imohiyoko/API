# 0007. プロセス内から操作を呼ぶ口を足し、gRPC・YAML は使う利用者だけが含めるパッケージに分ける

- 状態: 承認
- 日付: 2026-11-26
- 関連: [design.md](../design.md) 2.2・2.3・3章、[roadmap.md](../roadmap.md) M3.5、#11・#12

## 背景

[imohiyoko/herdrOrchestrator](https://github.com/imohiyoko/herdrOrchestrator) の ADR 0008 で、CLI（`tb`、1回ごとに別プロセス）と Web UI の下に置くアプリケーション API を、このライブラリの `define.Query`・`define.Command` で書く案が出た。

- 口が REST（`Server.Handler`）と gRPC だけなので、CLI から操作を呼ぶには、毎回 HTTP の口を立てるか、ハンドラーを直接呼んで認証・認可・検証を飛ばすしかなかった
- `runtime` が gRPC の口を持ち、`manifest` が YAML を読むので、REST だけを使う利用者のバイナリにも grpc-go・protobuf・YAML のライブラリが入っていた。herdr は 1 つのバイナリで配る

## 決定

- `Server.Invoke(ctx, 公開範囲, 操作の名前, トークン, 入力)` を足す。REST・gRPC と同じ共通の層（認証 → 主体の確認 → レートリミット → 入力 → 認可 → 実行 → 出力の絞り込み）を通り、プロトコルの都合（パス・ヘッダー・metadata）だけを持たない
  - 公開範囲は必ず渡す。その公開範囲に `Expose` していない操作は `not_found`
  - 認証はトークンで行う。確かめ済みの主体を渡す形は作らない
  - 入力は In 型の値か JSON の両方を受ける。エラーは開示のルールを通した `*define.Error`
- プロトコルによらない1回の呼び出しを `Server.Serve(ctx, 公開範囲, 操作の名前, runtime.Request)` として公開し、REST 以外のプロトコルの口はこの上に作る
- gRPC の口を `runtime/grpcx`（`grpcx.Server(srv, 公開範囲)`）、YAML の読み込みを `manifest/yamlx` に移す。`Server.GRPCServer`・`manifest.ReadYAML`・`manifest.ReadFile` は残さない
- `define`・`manifest`・`runtime`・`port` は grpc・protobuf・yaml を import しないことを `internal/archtest` で検査する

## 結果

- CLI・テスト・操作の組み合わせ・バッチから、REST と同じ認証・認可・検証で操作を呼べる。REST と Invoke で同じ入力・主体なら同じ出力とエラーになる
- REST と Invoke だけを使う利用者のビルドに grpc-go・protobuf・YAML のライブラリが入らない
- `Server.GRPCServer` などを使っていた呼び出し元は書き換えが要る（破壊的変更）。まだ設計段階で、呼び出し元はこのリポジトリの中だけなので、非推奨の期間は置かない
- Go の値の入力では、公開範囲に見えないフィールドを無いものとして扱う（REST の JSON なら `unknown field` になる入力も通る）。ポインターでないフィールドはゼロ値でもあるものとする（gRPC と同じ）

## 検討した代替案

- 確かめ済みの `port.Principal` を渡して呼ぶ口：認証を飛ばす道になり、トークンの対象（`AudienceConfig.Resource`）や口ごとの主体の確認も飛ぶ
- 既定の公開範囲（internal）を置く：呼び出し元が意識せずに internal のフィールドを受け取り、public に出してしまう
- `Server.GRPCServer` を残して非推奨にする：メソッドがあるかぎり `runtime` が grpc-go を import し、依存を分けられない
- ビルドタグで gRPC を外す：利用者がタグを付け忘れると黙って依存が入り、`go list` でも検査しにくい
