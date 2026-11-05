package runtime_test

import (
	"context"
	"testing"

	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"
	"github.com/imohiyoko/API/runtime"
)

// roadmap.md M2：public の口に internal 用のサービスアカウントのトークンを送ると拒む。
// 口ごとに iam のリソース（RFC 8707）を分け、トークンの aud で見分ける。
func TestResourceSeparatesAudiences(t *testing.T) {
	get := define.Query[struct{}, string]("things.get",
		define.Authz("things:read"),
		define.Expose(define.Internal, define.REST),
		define.Expose(define.Public, define.REST),
	).Handle(func(context.Context, struct{}) (string, error) { return "ok", nil })
	authn := memory.NewAuthenticator().
		Add("internal-sa", port.Principal{Type: port.PrincipalServiceAccount, ID: "sva_1", Audiences: []string{"https://things.internal"}}).
		Add("public-sa", port.Principal{Type: port.PrincipalServiceAccount, ID: "sva_2", Audiences: []string{"https://things.example"}}).
		Add("no-aud", port.Principal{Type: port.PrincipalServiceAccount, ID: "sva_3"})
	authz := memory.NewAuthorizer()
	for _, id := range []string{"sva_1", "sva_2", "sva_3"} {
		authz.Allow(memory.Grant{PrincipalID: id, Action: "things:read"})
	}
	srv, err := runtime.Build(define.NewRegistry().Add(get), runtime.Config{
		Authenticator: authn, Authorizer: authz,
		Audiences: map[string]runtime.AudienceConfig{
			define.Internal: withResource(define.Internal, "https://things.internal"),
			define.Public:   withResource(define.Public, "https://things.example"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		aud, token string
		status     int
	}{
		{define.Internal, "internal-sa", 200},
		{define.Public, "public-sa", 200},
		{define.Public, "internal-sa", 401},
		{define.Internal, "public-sa", 401},
		{define.Public, "no-aud", 401},
	}
	for _, c := range cases {
		res := call(t, srv.Handler(c.aud), "GET", "/things", c.token, "")
		if res.status != c.status {
			t.Errorf("%s with %s: %d %s, want %d", c.aud, c.token, res.status, res.raw, c.status)
		}
	}
}

func withResource(aud, resource string) runtime.AudienceConfig {
	ac := runtime.DefaultAudienceConfig(aud)
	ac.Resource = resource
	return ac
}
