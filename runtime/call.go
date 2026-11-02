package runtime

import (
	"context"
	"errors"
	"slices"

	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/manifest"
	"github.com/imohiyoko/API/port"
)

// opCall は、プロトコルによらない1回の呼び出しの流れ（認証 → 主体の確認 → 入力 → 認可 → 実行 → 出力の絞り込み）。
// REST と gRPC は、この上で入力の取り出しと結果の書き出しだけを持つ。
type opCall struct {
	m    *manifest.Manifest
	aud  string
	ac   AudienceConfig
	spec *manifest.Operation
	op   define.Operation
	cfg  Config
}

// run は呼び出しを行い、公開範囲に見えるフィールドだけの出力（define.Encode の結果）を返す。
func (c *opCall) run(ctx context.Context, token string, hasToken bool, src define.InputSource) (any, error) {
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
		if !slices.Contains(c.ac.Principals, p.Type) || (c.ac.RequireOrg && p.OrgID == "") {
			// この口で受けない主体（例：internal の口にユーザーのトークン）。
			return nil, &define.Error{Code: define.CodePermissionDenied, Reasons: []string{"principal_not_allowed"}}
		}
		principal = &p
		ctx = define.WithPrincipal(ctx, p)
	} else if !c.spec.Anonymous {
		return nil, &define.Error{Code: define.CodeUnauthenticated}
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
