# API Design Doc

- Status: Draft / Last updated: 2026-11-02
- このドキュメントは「今どうなっているか」だけを書く。判断の理由は [ADR](adr/README.md)、フェーズごとのゴールは [roadmap.md](roadmap.md)

## 1. 目的

1つの操作（エンドポイント）を1回だけ定義し、「どの公開範囲に、どのプロトコルで出すか」を書けば、それぞれの API が自動で作られるようにする。

- **作るもの**：公開範囲ごとの口（listener）、プロトコルごとのサーバー、契約ファイル（OpenAPI・.proto・GraphQL SDL・WSDL・AsyncAPI）、ドキュメント
- **任せるもの**：認証・認可は [imohiyoko/iam](https://github.com/imohiyoko/iam)（ADR 0004）
- **当面やらない**：SDK の生成、SOAP（M6 以降）、API ゲートウェイとしての単独運用

## 2. 基本方針

### 2.1 定義の単位は「操作」

操作は、入力・出力の型、ハンドラー、認可の宣言、公開の許可を1か所に持つ（ADR 0001）。URL・gRPC のメソッド名・GraphQL のフィールド名などのプロトコルの都合は、操作の名前と種類から決め、合わないときだけ上書きする。

操作の種類は4つ。

| 種類 | 意味 |
|---|---|
| Query | 安全な読み取り |
| Command | 更新 |
| Stream | 呼び出し元が購読し、サーバーが続けて送る |
| Event | サーバー側で起きたことを通知する |

### 2.2 定義 → マニフェスト → 実行

```
Go の定義 ──書き出し──▶ マニフェスト（JSON / YAML。言語に依存しない）
                              │
               ┌──────────────┼───────────────┐
               ▼              ▼               ▼
        契約ファイルの生成   Go のライブラリ    サイドカー（ほかの言語）
        （CLI）             （組み込み）       （別プロセス）
```

- 定義は Go で書く。Go 以外のチームはマニフェストを YAML で直接書いてもよい（ADR 0002）
- マニフェストが公開範囲・プロトコル・フィールドの公開範囲・認可の宣言をすべて持つ。契約ファイルの生成と、実行時の判断はマニフェストだけを見る
- 実行は2通り（ADR 0003）
  - **ライブラリ**：Go のサービスに組み込む。ハンドラーは Go の関数
  - **サイドカー**：Go 以外のサービスの隣で動く。認証・認可・プロトコルの変換・フィールドの絞り込みをして、本体のハンドラーには単純な HTTP（JSON）で渡す
- 必要になれば、ほかの言語の薄いライブラリをマニフェストの上に足す

### 2.3 リポジトリ構成（予定）

```
api/
├── define/      # 定義の DSL（Go）
├── manifest/    # マニフェストの型、読み書き、検証
├── runtime/     # 共通の層（検証・認証・認可・制限・監査）と公開範囲ごとの組み立て
├── protocol/    # rest/ grpc/ graphql/ websocket/ webhook/
├── gen/         # 契約ファイルの生成（openapi/ proto/ sdl/ asyncapi/）
├── port/        # Authenticator・Authorizer など外部依存のポート
├── adapters/    # iam/ memory/
├── cmd/api/     # CLI（gen・check・sidecar）
└── docs/
```

- コア（`define`・`manifest`・`runtime`）は `adapters/` を import しない
- プロトコルとアダプタは、使うものだけをビルドに含められるよう別パッケージにする

## 3. 定義

```go
var GetUser = define.Query[GetUserIn, User]("users.get",
    define.Doc("ユーザーを取得する"),
    define.Authz("users:read", define.Resource("user", "ID")),

    // 公開の許可：公開範囲 × プロトコル
    define.Expose(define.Internal, define.GRPC, define.REST),
    define.Expose(define.Partner, define.REST, define.GraphQL),
    define.Expose(define.Public, define.REST),

    // 上書きが要るときだけ
    define.HTTP("GET /v1/users/{id}"),
    define.RateLimit(define.Public, "60/min"),
).Handle(func(ctx context.Context, in GetUserIn) (User, error) {
    // 業務ロジック
})

type GetUserIn struct {
    ID string `api:"id,path"`
}

type User struct {
    ID           string `api:"id"`
    Name         string `api:"name"`
    Email        string `api:"email,min=partner"` // public には出さない
    InternalNote string `api:"note,min=internal"` // internal だけ
}
```

- 定義はレジストリに集め、`runtime.Build(registry, config)` が公開範囲ごとの `http.Handler`・gRPC サーバーを返す
- `api gen` がマニフェストと契約ファイルを書き出す。`api check` が不正な定義（4.3）を報告する

## 4. 公開範囲

### 4.1 既定の公開範囲

公開範囲は internal ⊇ partner ⊇ public の順に狭くなる。ホスト先は独自の公開範囲（例：`admin`）を足せ、順序の中の位置を決める。

| 項目 | internal | partner | public |
|---|---|---|---|
| 呼び出し元 | 社内のサービス | 提携先の Org | 一般の開発者・エンドユーザー |
| 認証（iam） | サービスアカウントのトークン | 提携先 Org のサービスアカウント（トークンか API キー） | ユーザーのアクセストークン（OIDC）か API キー |
| 口 | 別の listener（社内網だけ） | 別の listener | 別の listener |
| フィールド | すべて | `min=partner` 以上 | `min=public` だけ |
| エラーの詳しさ | 拒否の理由コードまで | 理由コード | 一般的なコードだけ |
| レートリミット | 既定なし | 提携先ごと | 主体・IP ごと |
| 契約の扱い | 破壊的変更を許す | バージョンを付けて予告 | バージョンを付け、広く保つ |
| 契約ファイル・ドキュメント | 社内向けに生成 | 生成して提携先に渡す | 生成して公開する |

- partner は、提携先を iam の Org として登録し、その Org のサービスアカウントで呼ぶ
- 公開範囲ごとに別の契約ファイルを作る（`openapi/public.yaml` には public の操作とフィールドしか載らない）

### 4.2 既定は公開しない

- `Expose` を書かない操作はどこにも出ない
- フィールドの `min` を書かなければ、その操作を出すすべての公開範囲に出る
- public に出す操作は `Authz` か、明示的な `define.Anonymous` が要る

### 4.3 定義の段で拒むもの

- 種類とプロトコルの組み合わせが 5.1 の表で「―」
- public に出すのに `Authz` も `Anonymous` も無い
- フィールドの `min` が、操作を出す公開範囲より広い（出しても見えないだけなので警告）
- 同じ公開範囲・プロトコルの中で、パス・メソッド名・フィールド名がぶつかる

## 5. プロトコル

### 5.1 操作の種類との対応

| 種類 | REST | GraphQL | gRPC | SOAP | WebSocket | Webhook |
|---|---|---|---|---|---|---|
| Query | GET | Query | unary | operation | ― | ― |
| Command | POST / PUT / PATCH / DELETE | Mutation | unary | operation | ― | ― |
| Stream | SSE | Subscription | server streaming | ― | メッセージ | ― |
| Event | ― | Subscription | server streaming | ― | メッセージ | POST（送信） |

### 5.2 実装と契約

| プロトコル | 契約 | 実装 |
|---|---|---|
| REST | OpenAPI 3.1 | `net/http`（Go 1.22 以降のパターン） |
| gRPC | .proto | grpc-go。生成コードを使わず、マニフェストから動的にメッセージを組み立てる |
| GraphQL | SDL | 未決（gqlgen か graphql-go） |
| WebSocket | AsyncAPI | coder/websocket |
| Webhook | AsyncAPI | 自前。署名（Standard Webhooks 形式）、再送、配信先の登録 |
| SOAP | WSDL | 自前（`encoding/xml`）。M6 以降 |

- Webhook は送る側なので、配信先の登録・一覧・削除の操作も自動で作り、その Event と同じ公開範囲に出す
- 入力の検証・認証・認可・レートリミット・監査ログは、プロトコルに入る前の共通の層（`runtime`）で行う

## 6. 認証と認可（iam）

ライブラリ・サイドカーは、iam に次の2つとして登録する。

- 機密の連携先（イントロスペクションを呼ぶため）
- サービスアカウント（権限 `authz:check`）

要求ごとの流れ：

1. 認証情報を取り出す（`Authorization: Bearer` のトークンか API キー）
2. iam のイントロスペクション（`POST /oauth2/introspect`、RFC 7662）で確かめ、主体（ユーザーかサービスアカウント）・Org・`sid` を得る。結果は iam の保証値（`session.validity_max_delay`、既定30秒）以内でキャッシュする
3. 公開範囲と主体の種類が合うか確かめる（public の口に internal 用のトークンは通さない）
4. `Authz` の宣言から `POST /v1/admin/authz:check` を呼ぶ。`access_token` を渡し、認証の文脈（`attested`）は iam に組み立てさせる
5. 拒否の理由コードを、公開範囲の開示のルール（4.1）に合わせて返す

外部依存はポート（`port.Authenticator`、`port.Authorizer`）に閉じ、iam はアダプタ（`adapters/iam`）に置く。テスト用にインメモリの実装を持つ。

### 6.1 iam に求めている機能

| issue | 内容 | 使う場面 |
|---|---|---|
| [iam#151](https://github.com/imohiyoko/iam/issues/151) | サービスアカウントのトークンと API キーをリソースサーバーが検証できる | internal・partner の認証 |
| [iam#152](https://github.com/imohiyoko/iam/issues/152) | `authz:check` の主体にサービスアカウント | internal・partner の認可 |
| [iam#153](https://github.com/imohiyoko/iam/issues/153) | 複数の判定をまとめる batch check | GraphQL・一覧 |
| [iam#154](https://github.com/imohiyoko/iam/issues/154) | アクセストークンを対象の API に限る（RFC 8707） | 公開範囲をまたぐトークンの持ち込みを防ぐ |

## 7. 未決事項

- GraphQL の実装ライブラリ
- マニフェストの形式の版の付け方と互換性のルール
- ライセンス
