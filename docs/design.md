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

#### サイドカー（`api sidecar`、`sidecar/`）

```
api -manifest api/manifest.yaml sidecar -upstream http://127.0.0.1:3000 \
    -listen internal=127.0.0.1:8090,partner=:8091,public=:8092 -grpc internal=127.0.0.1:9090
```

- マニフェスト（JSON か YAML）の操作を、ライブラリと同じ `runtime` で動かす。認証・認可・レートリミット・入力の検証・フィールドの絞り込み・エラーの開示・REST と gRPC はライブラリと同じ実装なので、挙動も同じ（`scripts/e2e-sidecar.sh` で、Node.js の本体と Go のライブラリの応答が一致することを確かめる）
- 本体への要求は操作ごとに `POST {upstream}/{操作の名前}`、本文は `{"operation", "audience", "principal": {"type", "id", "org_id"} か null, "input"}`。`input` は検証済みで、要求を受けた公開範囲に見えるフィールドだけ。トークンは渡さない
- 本体の応答は、成功なら 200 で `{"output": …}`、失敗なら 4xx・5xx で `{"error": {"code", "message", "reasons"}}`（`code` は 3.2 のコード。知らないコード、`unauthenticated`（呼び出し元は認証済みなので、サイドカーと本体の間の設定の誤り）、壊れた応答、200 で `output` が無い応答は `internal`）。本体に届かなければ `unavailable`
- 本体の出力はマニフェストの出力の型で確かめ、公開範囲に見えるフィールドだけを返す。マニフェストに無いフィールドや見せないフィールドを本体が返しても、呼び出し元には出ない。型が合わない・必須のフィールドが無ければ `internal`
- 本体は、`API_SIDECAR_UPSTREAM_TOKEN` を `Authorization: Bearer` で受け取り、サイドカーからの要求だけを受ける（本体はループバックで待ち受ける）
- 認証・認可は、`IAM_PUBLIC_URL` などがあれば iam（6章。`IAM_RESOURCE_<公開範囲>` で口ごとのリソース）、`-dev` なら開発用のトークン
- ライブラリとの違い：Go の型を持たないので、`null` は `optional` のフィールドならどれでも受ける（Go ではポインターのフィールドだけ）

### 2.3 リポジトリ構成

```
api/
├── define/      # 定義の DSL（Go）、入力の組み立てと出力の絞り込み
├── manifest/    # マニフェストの型、読み書き、検査（4.3）、REST・gRPC の規則（5.1）
├── runtime/     # 共通の層（検証・認証・認可・エラーの開示）と公開範囲ごとの口（REST・gRPC）
├── gen/         # 契約ファイルの生成（openapi/ proto/。今後 sdl/ asyncapi/）
├── scripts/     # E2E（e2e-grpc.sh・e2e-iam.sh・e2e-sidecar.sh）
├── port/        # Authenticator・Authorizer など外部依存のポート
├── adapters/    # iam/ memory/
├── cli/         # check・manifest・gen（利用者の main から cli.Main(registry, args) で呼ぶ）
├── cmd/api/     # マニフェストのファイル（JSON・YAML）を扱う CLI と sidecar
├── sidecar/     # サイドカー：マニフェストの操作を本体への HTTP（JSON）の呼び出しにする
├── examples/    # 例（users、Node.js の本体の users-node）
├── internal/archtest/  # モジュール境界の検査
└── docs/
```

- コア（`define`・`manifest`・`runtime`・`port`・`gen`・`cli`・`sidecar`）は `adapters/` を import しない（`internal/archtest` で検査）
- Go 1.26 以上（grpc-go が求める版）。依存は grpc-go・protobuf と、YAML のマニフェストを読む go.yaml.in/yaml/v3 だけ。REST だけを使う利用者も、この3つをビルドに含む
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

- 定義はレジストリに集め、`runtime.Build(registry, config)` が公開範囲ごとの口を作る。`Server.Handler(公開範囲)` が REST の `http.Handler`、`Server.GRPCServer(公開範囲)` が gRPC の `*grpc.Server`
- レジストリには API の名前を付けられる（`define.NewRegistry(define.WithName("users"))`、マニフェストの `name`）。gRPC のパッケージ名は「API の名前.公開範囲」（例：`users.internal`）。省略すると `api`
- 利用者の main から `cli.Main(registry, args)` を呼ぶと、`check`・`manifest`・`gen` が使える。`gen` は `manifest.json` と、公開範囲ごとの `openapi/<公開範囲>.json`（REST）・`proto/<公開範囲>.proto`（gRPC）を書き出す。そのプロトコルで何も出さない公開範囲のファイルは作らず、前に作ったものは消す。`cmd/api` は同じことをマニフェストのファイルから行う

### 3.1 フィールドのタグ

API に出すフィールドには `api` タグを付ける。タグの無いフィールドは出さない（4.2）。

| タグ | 意味 |
|---|---|
| `api:"name"` | API 上の名前 |
| `api:"id,path"` | 入力の場所（`path`・`query`・`body`）。既定は Query なら `query`、Command なら `body`。入力の一番上のフィールドでだけ効く |
| `api:"email,min=partner"` | 見せる最も狭い公開範囲。それより狭い公開範囲では、出力から落とし、入力では無いものとして扱う |
| `api:"note,optional"` | 省略できる。ポインターの型も省略できる |

対応する型は、文字列・真偽値・整数・浮動小数点数・`time.Time`（RFC 3339）・`[]byte`・スライス・struct とそのポインター。map と再帰する型は定義のエラーにする。

### 3.2 ハンドラーのエラーと文脈

- ハンドラーは `define.Errorf(define.CodeNotFound, ...)` のように種類の付いたエラーを返す。種類の無いエラーは `internal` として扱い、中身は見せない
- `define.PrincipalFrom(ctx)` で認証済みの主体、`define.AudienceFrom(ctx)` で要求を受けた公開範囲を取れる

## 4. 公開範囲

### 4.1 既定の公開範囲

公開範囲は internal ⊇ partner ⊇ public の順に狭くなる。ホスト先は独自の公開範囲（例：`admin`）を足せ、順序の中の位置を決める。

| 項目 | internal | partner | public |
|---|---|---|---|
| 呼び出し元 | 社内のサービス | 提携先の Org | 一般の開発者・エンドユーザー |
| 認証（iam） | サービスアカウントのトークン | 提携先 Org のサービスアカウント（トークンか API キー） | iam（OIDC Provider）が発行した OAuth 2.0 のアクセストークンか API キー。ID トークンは受けない |
| 口 | 別の listener（社内網だけ） | 別の listener | 別の listener |
| フィールド | すべて | `min=partner` 以上 | `min=public` だけ |
| エラーの詳しさ | 拒否の理由コードまで | 理由コード | 一般的なコードだけ |
| レートリミット | 既定なし | 提携先ごと | 主体・IP ごと |
| 契約の扱い | 破壊的変更を許す | バージョンを付けて予告 | バージョンを付け、広く保つ |
| 契約ファイル・ドキュメント | 社内向けに生成 | 生成して提携先に渡す | 生成して公開する |

- partner は、提携先を iam の Org として登録し、その Org のサービスアカウントで呼ぶ
- 公開範囲ごとに別の契約ファイルを作る（`openapi/public.json` には public の操作とフィールドしか載らない）
- 口ごとに受ける主体の種類を決める（`runtime.AudienceConfig`）。既定は internal・partner がサービスアカウントだけ（partner は Org に属するものだけ）、public がユーザーとサービスアカウント。受けない主体は 403（理由 `principal_not_allowed`）
- 入力の誤り（`invalid_argument`）は、直し方が分かるように、どの公開範囲でもメッセージを見せる

### 4.2 既定は公開しない

- `Expose` を書かない操作はどこにも出ない
- フィールドの `min` を書かなければ、その操作を出すすべての公開範囲に出る
- どの公開範囲に出す操作にも `Authz` か、明示的な `define.Anonymous` が要る。公開サイトの表示やログイン・トークン発行のように認可が要らない操作は `Anonymous` を書く

### 4.3 定義の検査

`api check` と起動時に検査する。エラーが1つでもあれば `api check` は 0 以外で終わり、起動も拒む。警告は報告するだけで、終了コードは 0（`--strict` で警告もエラーにする）。

エラー：

- 種類とプロトコルの組み合わせが 5.1 の表で「―」
- 名前の形、公開範囲・プロトコルの名前、入力の場所の誤り（Query の body など）、`Authz` と `Anonymous` の併用、`Authz` のリソースのフィールドが入力に無い
- どこかの公開範囲に出すのに `Authz` も `Anonymous` も無い
- REST に出す Command の HTTP メソッドが決まらない（5.1）
- 同じ公開範囲・プロトコルの中で、パス・メソッド名・メッセージ名・フィールド名がぶつかる
- gRPC に出す操作の、protobuf で表せない型（配列の配列）や名前（protobuf の識別子でないフィールド名）

警告：

- フィールドの `min` が、操作を出すどの公開範囲よりも狭い（どこにも見えないフィールドになる）
- まだ実装していないプロトコルへの公開（その公開は無視する）。gRPC の Stream・Event もまだ（M5）

`Authz` も `Anonymous` も無い操作は、public 以外でもエラーにする。partner の口では、通してしまうとどの提携先 Org からでも呼べてしまうため（脆弱性診断 2026-11-04 の指摘）。この検査は `api check` と起動時に行うもので、個々の呼び出しを拒むものではない。

レートリミットの誤り（知らない公開範囲、同じ公開範囲に2つ、読めない上限）はエラー、操作を出していない公開範囲へのレートリミットは警告。

### 4.4 レートリミット

- 操作ごと・公開範囲ごとに `define.RateLimit(define.Partner, "600/min")` で決める（マニフェストの `rate_limits`）。単位は `s`・`min`・`h`。書かない公開範囲では数えない
- 呼び出し元ごとに数える。Org を求める公開範囲（partner）は Org ごと、それ以外は主体ごと、匿名の呼び出しは接続元のアドレスごと。Org ごとなので、ある提携先が上限に達しても、ほかの提携先は影響を受けない。同じ Org のサービスアカウントは上限を分け合う
- REST と gRPC は同じ上限を分け合う（数える単位は公開範囲・操作・呼び出し元）
- 認証と主体の確認のあと、入力の検証と認可の前に数える。超えた呼び出しで iam を呼ばない
- 超えたら `resource_exhausted`（REST は 429 と `Retry-After`、gRPC は `RESOURCE_EXHAUSTED` と `google.rpc.RetryInfo`）。拒否の理由は `rate_limited`
- 既定はプロセス内のトークンバケット（続けて上限の回数まで呼べ、期間に応じて戻る）。複数のプロセスで動かすときは、共有の保存先を使う `port.RateLimiter` の実装を `Config.RateLimiter` に渡す

## 5. プロトコル

### 5.1 操作の種類との対応

| 種類 | REST | GraphQL | gRPC | SOAP | WebSocket | Webhook |
|---|---|---|---|---|---|---|
| Query | GET | Query | unary | operation | ― | ― |
| Command | 下の規則で決める | Mutation | unary | operation | ― | ― |
| Stream | SSE | Subscription | server streaming | ― | メッセージ | ― |
| Event | ― | Subscription | server streaming | ― | メッセージ | POST（送信） |

- SSE は独立したプロトコルではなく、REST の Stream の実装として扱う（M5 で足す）
- REST のパスは `define.HTTP` で指定する。省略したときは名前の語を `/` でつなぎ（最後の語が get・list・create・replace・update・delete なら落とす）、path のフィールドを `/{name}` で足す。例：`users.get` → `GET /users/{id}`、`users.list` → `GET /users`
- gRPC のサービス名とメソッド名は、操作の名前の最後の語をメソッド、それより前の語をサービスにして PascalCase にする。例：`users.get` → `Users/Get`、`orgs.members.list` → `OrgsMembers/List`。語が1つなら、サービスは `Default`。要求・応答のメッセージは `UsersGetRequest`・`UsersGetResponse`
- REST の Command の HTTP メソッドは、`define.HTTP` で指定する。省略したときは操作の名前の最後の語で決める：`create` → POST、`replace` → PUT、`update` → PATCH、`delete` → DELETE。どれにも当たらなければエラー（4.3）。冪等性は PUT・DELETE だけを冪等として契約に書く

### 5.2 実装と契約

| プロトコル | 契約 | 実装 |
|---|---|---|
| REST | OpenAPI 3.1（JSON） | `net/http`（Go 1.22 以降のパターン） |
| gRPC | .proto | grpc-go。生成コードを使わず、マニフェストから動的にメッセージを組み立てる |
| GraphQL | SDL | 未決（gqlgen か graphql-go） |
| WebSocket | AsyncAPI | coder/websocket |
| Webhook | AsyncAPI | 自前。署名（Standard Webhooks 形式）、再送、配信先の登録 |
| SOAP | WSDL | 自前（`encoding/xml`）。M6 以降 |

- Webhook は送る側なので、配信先の登録・一覧・削除の操作も自動で作り、その Event と同じ公開範囲に出す
  - これらの操作は Event の `Anonymous` を継承しない。権限 `webhooks:manage` の `Authz` を持ち、配信先は登録した主体の Org（Org が無ければ主体）をリソースとして検査する。ほかの Org の配信先は見えない
- 入力の検証・認証・認可・レートリミット・監査ログは、プロトコルに入る前の共通の層（`runtime`）で行う

#### gRPC

- `.proto` と実際の口は、同じ記述子（`gen/proto`）から作るので食い違わない。生成コードは要らず、要求・応答は `dynamicpb` で組み立てる
- 入力の場所（path・query・body）は gRPC には無いので、すべてのフィールドを要求のメッセージに入れる
- 型：文字列 → `string`、真偽値 → `bool`、整数 → `int32`・`int64`、浮動小数点数 → `float`・`double`、`time.Time` → `google.protobuf.Timestamp`、`[]byte` → `bytes`、スライス → `repeated`、struct → 入れ子のメッセージ（名前はフィールド名の PascalCase）。省略できるスカラーは proto3 の `optional`
- オブジェクトでない出力（一覧など）は、応答のメッセージの `value` に包む
- フィールドの番号は、定義の中のフィールドの順番（1から）。公開範囲で見えないフィールドを除いても番号は変わらないので、公開範囲ごとの `.proto` の間でも同じ番号になる。フィールドの順番を変える・途中に足すと互換性が壊れる（足すときは最後に足す）
- `json_name` はフィールド名と同じにする。grpcurl や JSON 変換でも REST と同じ名前になる
- proto3 では、スカラーが「無い」ことと「ゼロ値」を区別できない。gRPC では、必須のスカラーはゼロ値として受ける（REST なら required のエラーになる入力も通る）。区別したいフィールドは省略できる（optional）ものにする
- 認証は metadata の `authorization: Bearer <token>`。エラーの対応は invalid_argument → `INVALID_ARGUMENT`、unauthenticated → `UNAUTHENTICATED`、permission_denied → `PERMISSION_DENIED`、not_found → `NOT_FOUND`、conflict → `ALREADY_EXISTS`、failed_precondition → `FAILED_PRECONDITION`、resource_exhausted → `RESOURCE_EXHAUSTED`、unavailable → `UNAVAILABLE`、internal → `INTERNAL`。見せる中身は REST と同じ開示のルール（4.1）で、メッセージは status の message、コードは `google.rpc.ErrorInfo` の reason、拒否の理由は ErrorInfo の metadata `reasons`（カンマ区切り）に入れる
- サーバーリフレクションは internal だけ既定で載せる（`AudienceConfig.GRPCReflection`）。載せると、契約を持たない呼び出し元にも操作と型が見えるため

## 6. 認証と認可（iam）

ライブラリ・サイドカーは、iam に次の2つとして登録する。

- 機密の連携先（イントロスペクションを呼ぶため）
- サービスアカウント（権限 `authz:check`）

要求ごとの流れ：

1. 認証情報を取り出す（`Authorization: Bearer` のトークン）
2. iam のイントロスペクション（`POST /oauth2/introspect`、RFC 7662）で確かめ、主体（ユーザーかサービスアカウント）・Org・`sid`・`aud`・`scope` を得る。結果は iam の保証値（`session.validity_max_delay`、既定30秒）以内でキャッシュする
3. 口の `Resource`（`runtime.AudienceConfig`）があれば、トークンの `aud` にそれがあるか確かめる。無ければ `unauthenticated`（`wrong_audience`）
4. 公開範囲と主体の種類が合うか確かめる（例：internal の口にユーザーのトークン）
5. `Authz` の宣言から判定する。ユーザーは `POST /v1/admin/authz:check` を呼び、`access_token` を渡して認証の文脈（`attested`）は iam に組み立てさせる。サービスアカウントは、iam の check が主体として受けるまで（iam#152）、トークンのスコープに `Authz` の操作（例：`users:read`）があるかで判定する（無ければ `scope_missing`）
6. 拒否の理由コードを、公開範囲の開示のルール（4.1）に合わせて返す

受けるトークン：

- ユーザー：iam が OIDC の連携先に発行したアクセストークン
- サービスアカウント：client credentials（iam の `POST /v1/admin/token`）で、iam に登録したリソース（RFC 8707、iam の ADR 0112）に向けて取ったトークン。リソースの URI を口ごとに分け（例：`https://users.internal` と `https://users.example`）、`Resource` に設定すれば、ほかの口・API に向けたトークンを持ち込めない。リソースのスコープには `Authz` の操作の名前を登録する
- 受けないもの：管理 API 向けのトークンと API キー（iam が `active: false` を返す）、リフレッシュトークン、Personal Access Token、DPoP・mTLS に結び付けたトークン（`cnf`。結び付けを確かめないので拒む）

外部依存はポート（`port.Authenticator`、`port.Authorizer`）に閉じ、iam はアダプタ（`adapters/iam`）に置く。テスト用にインメモリの実装を持つ。
iam を起動した E2E は `scripts/e2e-iam.sh`（CI）。

### 6.1 iam に求めている機能

| issue | 内容 | 使う場面 | 状態 |
|---|---|---|---|
| [iam#151](https://github.com/imohiyoko/iam/issues/151) | サービスアカウントのトークンをリソースサーバーが検証できる | internal・partner の認証 | iam の ADR 0112（リソースに向けたトークン）で使える。API キーは管理 API 専用のまま |
| [iam#152](https://github.com/imohiyoko/iam/issues/152) | `authz:check` の主体にサービスアカウント | internal・partner の認可 | 未。それまではスコープで判定 |
| [iam#153](https://github.com/imohiyoko/iam/issues/153) | 複数の判定をまとめる batch check | GraphQL・一覧 | 未 |
| [iam#154](https://github.com/imohiyoko/iam/issues/154) | アクセストークンを対象の API に限る（RFC 8707） | 公開範囲をまたぐトークンの持ち込みを防ぐ | iam の ADR 0112 で使える（口の `Resource`） |
| [iam#190](https://github.com/imohiyoko/iam/issues/190) | サービスアカウントのトークンのイントロスペクションに `org_id` | partner（Org ごとの受け付けとレートリミット） | 未。それまではサービスアカウントで partner の口を呼べない |

## 7. 未決事項

- GraphQL の実装ライブラリ
- マニフェストの形式の版の付け方と互換性のルール
- ライセンス
