package define

import (
	"context"

	"github.com/imohiyoko/API/port"
)

type principalKey struct{}

// WithPrincipal は ctx に認証済みの主体を入れる。runtime が使う。
func WithPrincipal(ctx context.Context, p port.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom はハンドラーの中で、認証済みの主体を返す。Anonymous の操作で認証が無ければ false。
func PrincipalFrom(ctx context.Context) (port.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(port.Principal)
	return p, ok
}

type audienceKey struct{}

// WithAudience は ctx に要求を受けた公開範囲を入れる。runtime が使う。
func WithAudience(ctx context.Context, audience string) context.Context {
	return context.WithValue(ctx, audienceKey{}, audience)
}

// AudienceFrom はハンドラーの中で、要求を受けた公開範囲を返す。
func AudienceFrom(ctx context.Context) string {
	a, _ := ctx.Value(audienceKey{}).(string)
	return a
}
