# 0006. プロトコルは REST と gRPC から始め、SOAP は最後にする

- 状態: 承認
- 日付: 2026-11-02
- 関連: [roadmap.md](../roadmap.md)

## 背景

6つのプロトコルを一度には作れない。

## 決定

REST → gRPC → GraphQL → WebSocket・Webhook・SSE の順に足し、SOAP は必要になってから足す。

## 結果

- public の主流（REST）と internal の主流（gRPC）を先に固められる
- SOAP の利用者は M6 まで待つ

## 検討した代替案

- 最初から全プロトコル：定義のモデルが固まる前に実装が広がりすぎる
