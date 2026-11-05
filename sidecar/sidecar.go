// Package sidecar は、Go 以外の本体の隣で動き、マニフェストの操作を本体への HTTP（JSON）の呼び出しにする
// （design.md 2.2、ADR 0003、roadmap.md M3）。
//
// 認証・認可・入力の検証・フィールドの絞り込み・プロトコルの変換は、ライブラリと同じ runtime が行う。
// 本体が受けるのは、検証済みで、要求を受けた公開範囲に見える入力だけ。本体が返した出力も、
// マニフェストの型で確かめ、公開範囲に見えるフィールドだけを呼び出し元に返す。
//
// 本体への要求（操作ごとに POST {Upstream}/{操作の名前}）：
//
//	{"operation": "users.get", "audience": "partner",
//	 "principal": {"type": "service_account", "id": "sva_…", "org_id": "org_…"},  // 匿名なら null
//	 "input": {"id": "usr_1"}}
//
// 本体の応答：成功なら 200 で {"output": …}。失敗なら 4xx・5xx で
// {"error": {"code": "not_found", "message": "…", "reasons": ["…"]}}（code は define.Code）。
package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/manifest"
)

// Config はサイドカーの設定。
type Config struct {
	// Upstream は本体の URL（例：http://127.0.0.1:3000）。
	Upstream string
	// Token は本体に Authorization: Bearer で送る秘密。本体はこれでサイドカーからの要求だけを受ける。空なら送らない。
	Token string
	// HTTPClient は本体を呼ぶクライアント。nil なら 15 秒で切れるもの。
	HTTPClient *http.Client
	// MaxResponse は本体の応答の上限（バイト）。0 なら 4 MiB。
	MaxResponse int64
}

// Registry はマニフェストの操作を、本体を呼ぶ操作にして集める。runtime.Build にそのまま渡せる。
func Registry(m *manifest.Manifest, cfg Config) (*define.Registry, error) {
	if cfg.Upstream == "" {
		return nil, errors.New("sidecar: Upstream is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.MaxResponse == 0 {
		cfg.MaxResponse = 4 << 20
	}
	reg := define.NewRegistry(define.WithName(m.Name), define.WithAudiences(m.Audiences...))
	for _, spec := range m.Operations {
		reg.Add(&operation{spec: spec, cfg: cfg})
	}
	return reg, nil
}

// operation は define.Operation の実装。
type operation struct {
	spec manifest.Operation
	cfg  Config
}

func (o *operation) Spec() (manifest.Operation, error) { return o.spec, nil }

func (o *operation) Decode(src define.InputSource) (any, error) {
	if o.spec.Input.Type != "object" {
		return nil, fmt.Errorf("sidecar: %s: input must be an object", o.spec.Name)
	}
	return decodeInput(o.spec.Input, src)
}

func (o *operation) ResourceID(in any, field string) string {
	m, _ := in.(map[string]any)
	switch v := m[field].(type) {
	case nil:
		return ""
	case time.Time:
		return v.Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(v)
	}
}

type principal struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	OrgID string `json:"org_id,omitempty"`
}

type request struct {
	Operation string     `json:"operation"`
	Audience  string     `json:"audience"`
	Principal *principal `json:"principal"`
	Input     any        `json:"input"`
}

type response struct {
	Output json.RawMessage `json:"output"`
	Error  *struct {
		Code    define.Code `json:"code"`
		Message string      `json:"message"`
		Reasons []string    `json:"reasons"`
	} `json:"error"`
}

var knownCodes = map[define.Code]bool{
	define.CodeInvalidArgument: true, define.CodeUnauthenticated: true, define.CodePermissionDenied: true,
	define.CodeNotFound: true, define.CodeConflict: true, define.CodeFailedPrecondition: true,
	define.CodeResourceExhausted: true, define.CodeUnavailable: true, define.CodeInternal: true,
}

func (o *operation) Call(ctx context.Context, in any) (any, error) {
	req := request{Operation: o.spec.Name, Audience: define.AudienceFrom(ctx), Input: in}
	if p, ok := define.PrincipalFrom(ctx); ok {
		// トークンは本体に渡さない（本体は iam を呼ばない）。
		req.Principal = &principal{Type: p.Type, ID: p.ID, OrgID: p.OrgID}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.cfg.Upstream, "/")+"/"+o.spec.Name, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	if o.cfg.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+o.cfg.Token)
	}
	resp, err := o.cfg.HTTPClient.Do(hr)
	if err != nil {
		return nil, &define.Error{Code: define.CodeUnavailable, Message: "upstream: " + err.Error()}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, o.cfg.MaxResponse+1))
	if err != nil {
		return nil, &define.Error{Code: define.CodeUnavailable, Message: "upstream: " + err.Error()}
	}
	if int64(len(b)) > o.cfg.MaxResponse {
		return nil, fmt.Errorf("sidecar: %s: upstream response is larger than %d bytes", o.spec.Name, o.cfg.MaxResponse)
	}
	var res response
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, fmt.Errorf("sidecar: %s: upstream returned %d with a body that is not JSON", o.spec.Name, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		if res.Error == nil || !knownCodes[res.Error.Code] {
			return nil, fmt.Errorf("sidecar: %s: upstream returned %d", o.spec.Name, resp.StatusCode)
		}
		return nil, &define.Error{Code: res.Error.Code, Message: res.Error.Message, Reasons: res.Error.Reasons}
	}
	return output{raw: res.Output, schema: o.spec.Output}, nil
}
