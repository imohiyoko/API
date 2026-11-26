package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/imohiyoko/API/define"
)

// InvokeClient は、Invoke の呼び出しの接続元（匿名の呼び出しのレートリミットでは、プロセス内の呼び出しをまとめて数える）。
const InvokeClient = "in-process"

// Invoke は、プロセス内から操作を呼ぶ口（REST・gRPC と並ぶ口）。CLI の入口、テスト、操作から別の操作を呼ぶとき、バッチに使う。
// REST の口と同じ流れ（認証 → 主体の確認 → レートリミット → 入力 → 認可 → 実行 → 出力の絞り込み）を通り、
// 同じ入力・主体なら REST と同じ出力とエラーになる。
//
//   - audience は必ず渡す（既定の公開範囲は無い）。その公開範囲にどのプロトコルでも出していない操作は not_found
//   - token は Config.Authenticator で確かめる。空なら、トークンの無い呼び出し（Anonymous の操作だけ通る）
//   - in は入力。操作の In 型の値（ポインターも可）か、JSON のオブジェクト（json.RawMessage・[]byte）。nil は空の入力。
//     Go の値は、公開範囲に見えないフィールドを無いものとして扱う（gRPC で、その公開範囲の .proto に無いフィールドと同じ）
//   - 出力は、公開範囲に見えるフィールドだけの値（define.Encode の結果。object は map[string]any）
//   - エラーは *define.Error。メッセージと理由は、REST と同じく公開範囲の Disclosure に従う
func (s *Server) Invoke(ctx context.Context, audience, operation, token string, in any) (any, error) {
	fields, ferr := s.inputFields(audience, in)
	out, err := s.Serve(ctx, audience, operation, Request{Token: token, HasToken: token != "", Client: InvokeClient, Fields: fields, Err: ferr})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// inputFields は Invoke の入力を、フィールドの名前 → JSON の値にする。
func (s *Server) inputFields(audience string, in any) (map[string]json.RawMessage, error) {
	var raw []byte
	switch v := in.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	default:
		enc, err := define.Encode(in, func(min string) bool { return s.manifest.Visible(min, audience) })
		if err != nil {
			return nil, err
		}
		obj, ok := enc.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("input must be a struct or a JSON object, got %T", in)
		}
		fields := make(map[string]json.RawMessage, len(obj))
		for k, v := range obj {
			if v == nil {
				continue // nil のポインターは省略と同じ
			}
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			fields[k] = b
		}
		return fields, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("must be a JSON object")
	}
	return fields, nil
}
