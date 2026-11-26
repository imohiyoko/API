package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/manifest"
	"github.com/imohiyoko/API/port"
)

// opCall は、プロトコルによらない1回の呼び出しの流れ（認証 → 主体の確認 → レートリミット → 入力 → 認可 → 実行 → 出力の絞り込み）。
// REST・gRPC・Invoke は、この上で入力の取り出しと結果の書き出しだけを持つ。
type opCall struct {
	m    *manifest.Manifest
	aud  string
	ac   AudienceConfig
	spec *manifest.Operation
	op   define.Operation
	cfg  Config
}

// run は呼び出しを行い、公開範囲に見えるフィールドだけの出力（define.Encode の結果）を返す。
// client は接続元のアドレス（匿名の呼び出しのレートリミットに使う）。
func (c *opCall) run(ctx context.Context, token string, hasToken bool, client string, src define.InputSource) (any, error) {
	ctx = define.WithAudience(ctx, c.aud)

	// 認証。Anonymous でも、トークンが付いていれば確かめる。
	var principal *port.Principal
	if hasToken {
		p, err := c.cfg.Authenticator.Authenticate(ctx, token)
		if errors.Is(err, port.ErrUnauthenticated) {
			return nil, &define.Error{Code: define.CodeUnauthenticated}
		}
		if err != nil {
			return nil, c.unavailable("authenticate", err)
		}
		if c.ac.Resource != "" && !slices.Contains(p.Audiences, c.ac.Resource) {
			// ほかの API・口に向けたトークン（RFC 8707）。RFC 6750 の invalid_token と同じく認証の失敗にする。
			return nil, &define.Error{Code: define.CodeUnauthenticated, Reasons: []string{"wrong_audience"}}
		}
		if !slices.Contains(c.ac.Principals, p.Type) || (c.ac.RequireOrg && p.OrgID == "") {
			// この口で受けない主体（例：internal の口にユーザーのトークン）。
			return nil, &define.Error{Code: define.CodePermissionDenied, Reasons: []string{"principal_not_allowed"}}
		}
		principal = &p
		ctx = define.WithPrincipal(ctx, p)
	} else if !c.spec.Anonymous {
		return nil, &define.Error{Code: define.CodeUnauthenticated}
	}

	// レートリミット（design.md 4.4）。認可の前に数え、超えた呼び出しで iam を呼ばない。
	if rate, ok := c.spec.RateFor(c.aud); ok {
		key := c.m.APIName() + "|" + c.aud + "|" + c.spec.Name + "|" + rateKey(c.ac, principal, client)
		allowed, retry, err := c.cfg.RateLimiter.Allow(ctx, key, port.Rate{Requests: rate.Requests, Per: rate.Per})
		if err != nil {
			return nil, c.unavailable("rate limit", err)
		}
		if !allowed {
			return nil, &define.Error{Code: define.CodeResourceExhausted, Reasons: []string{"rate_limited"}, RetryAfter: retry}
		}
	}

	in, err := c.op.Decode(src)
	if err != nil {
		return nil, err
	}

	// 認可（iam の authz:check）。
	if a := c.spec.Authz; a != nil {
		req := port.CheckRequest{Principal: *principal, Action: a.Action, ResourceType: a.ResourceType}
		if a.ResourceField != "" {
			req.ResourceID = c.op.ResourceID(in, a.ResourceField)
		}
		d, err := c.cfg.Authorizer.Check(ctx, req)
		if err != nil {
			return nil, c.unavailable("authorize", err)
		}
		if !d.Allowed {
			return nil, &define.Error{Code: define.CodePermissionDenied, Reasons: d.Reasons}
		}
	}
	out, err := c.op.Call(ctx, in)
	if err != nil {
		return nil, err
	}
	return define.Encode(out, func(min string) bool { return c.m.Visible(min, c.aud) })
}

func (c *opCall) unavailable(step string, err error) error {
	c.cfg.Logger.Error("api dependency failed", "operation", c.spec.Name, "step", step, "error", err)
	return define.Errorf(define.CodeUnavailable, "%s failed", step)
}

// public は、エラーを公開範囲に見せてよい形の define.Error にする（failure と disclose を合わせたもの）。
func (c *opCall) public(err error) *define.Error {
	e := c.failure(err)
	msg, reasons := c.disclose(e)
	return &define.Error{Code: e.Code, Message: msg, Reasons: reasons, RetryAfter: e.RetryAfter}
}

// failure はエラーを define.Error にする。それ以外のエラーは internal にして、中身はログにだけ出す。
func (c *opCall) failure(err error) *define.Error {
	var e *define.Error
	if !errors.As(err, &e) {
		c.cfg.Logger.Error("api handler error", "operation", c.spec.Name, "error", err)
		return &define.Error{Code: define.CodeInternal}
	}
	return e
}

// disclose は、公開範囲の Disclosure（design.md 4.1）に従って、見せるメッセージと理由を返す。
func (c *opCall) disclose(e *define.Error) (message string, reasons []string) {
	if c.ac.Disclosure >= DiscloseReasons {
		reasons = e.Reasons
	}
	// 入力の誤りは、どの公開範囲でも直し方が分かるようにメッセージを見せる。
	if c.ac.Disclosure >= DiscloseDetail || e.Code == define.CodeInvalidArgument {
		message = e.Message
	}
	return message, reasons
}

// rateKey は数える単位。Org を求める公開範囲（partner）は Org ごと、それ以外は主体ごと、匿名なら接続元ごと。
// Org ごとに数えるので、ある提携先が上限に達しても、ほかの提携先は影響を受けない。
func rateKey(ac AudienceConfig, p *port.Principal, client string) string {
	switch {
	case p == nil:
		return "addr:" + client
	case ac.RequireOrg && p.OrgID != "":
		return "org:" + p.OrgID
	}
	return p.Type + ":" + p.ID
}

// Request は、プロトコルの口が Serve に渡す1回の呼び出し。プロトコルの都合（パス・ヘッダー・metadata）は持たない。
type Request struct {
	// Token は Bearer のトークン。HasToken が false なら、トークンの無い呼び出し（Anonymous の操作だけ通る）。
	Token    string
	HasToken bool
	// Client は接続元（匿名の呼び出しのレートリミットの単位）。
	Client string
	// Fields は入力のフィールド（API 上の名前 → JSON の値）。パス・クエリ・本文には、入力の定義に従って Serve が振り分ける。
	// 入力の定義に無い名前は、本文の知らないフィールドとして誤りにする。
	Fields map[string]json.RawMessage
	// Err は入力を読めなかったときの誤り。REST と同じく、認証と主体の確認のあとで invalid_argument になる。
	Err error
}

// Serve は、公開範囲に出した操作を1回呼ぶ。REST の口と同じ流れ（認証 → 主体の確認 → レートリミット → 入力 → 認可 → 実行 → 出力の絞り込み）を通る。
// gRPC（runtime/grpcx）などのプロトコルの口と Invoke が使う。
//
// 出力は公開範囲に見えるフィールドだけの値（define.Encode の結果）。エラーは、公開範囲の Disclosure（design.md 4.1）に従って
// メッセージと理由を落とした define.Error。その公開範囲に出していない操作は not_found。ハンドラーの panic は internal にする。
func (s *Server) Serve(ctx context.Context, audience, operation string, req Request) (out any, err *define.Error) {
	c, ok := s.calls[audience][operation]
	if !ok {
		return nil, &define.Error{Code: define.CodeNotFound}
	}
	defer func() {
		if v := recover(); v != nil {
			c.cfg.Logger.Error("api handler panic", "operation", c.spec.Name, "panic", v)
			out, err = nil, c.public(define.Errorf(define.CodeInternal, "handler panicked"))
		}
	}()
	o, e := c.run(ctx, req.Token, req.HasToken, req.Client, newFieldsSource(c, req))
	if e != nil {
		return nil, c.public(e)
	}
	return o, nil
}

// fieldsSource は、パス・クエリ・本文の区別の無い入力（gRPC のメッセージ、Invoke の値）を define.InputSource にする。
type fieldsSource struct {
	c      *opCall
	values map[string]json.RawMessage
	body   map[string]json.RawMessage
	err    error
}

func newFieldsSource(c *opCall, req Request) *fieldsSource {
	s := &fieldsSource{c: c, values: req.Fields, err: req.Err}
	for name, v := range req.Fields {
		i := slices.IndexFunc(c.spec.Input.Fields, func(f manifest.Field) bool { return f.Name == name })
		if i >= 0 && c.spec.Input.Fields[i].In != "body" {
			continue
		}
		if s.body == nil {
			s.body = map[string]json.RawMessage{}
		}
		s.body[name] = v
	}
	return s
}

func (s *fieldsSource) PathValue(name string) string {
	v, _ := s.QueryValues(name)
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (s *fieldsSource) QueryValues(name string) ([]string, bool) {
	raw, ok := s.values[name]
	if !ok {
		return nil, false
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		out := make([]string, len(arr))
		for i, r := range arr {
			out[i] = rawString(r)
		}
		return out, true
	}
	return []string{rawString(raw)}, true
}

// Body は body のフィールド。define の Decode は最初にこれを呼ぶので、入力を読めなかった誤りもここで返す。
func (s *fieldsSource) Body() (map[string]json.RawMessage, error) { return s.body, s.err }

func (s *fieldsSource) Visible(min string) bool { return s.c.m.Visible(min, s.c.aud) }

func rawString(r json.RawMessage) string {
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	return string(r)
}
