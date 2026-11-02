package runtime_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/port"
	"github.com/imohiyoko/API/runtime"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func limitedServer(t *testing.T, c *clock) *runtime.Server {
	t.Helper()
	get := define.Query[struct{}, string]("things.get",
		define.Authz("things:read"),
		define.Expose(define.Partner, define.REST),
		define.Expose(define.Public, define.REST, define.GRPC),
		define.RateLimit(define.Partner, "2/min"),
		define.RateLimit(define.Public, "1/s"),
	).Handle(func(context.Context, struct{}) (string, error) { return "ok", nil })
	authn := memory.NewAuthenticator().
		Add("org-a-1", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_a1", OrgID: "org_a"}).
		Add("org-a-2", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_a2", OrgID: "org_a"}).
		Add("org-b", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_b", OrgID: "org_b"}).
		Add("user", port.Principal{Type: port.PrincipalUser, ID: "usr_1"})
	authz := memory.NewAuthorizer()
	for _, id := range []string{"sa_a1", "sa_a2", "sa_b", "usr_1"} {
		authz.Allow(memory.Grant{PrincipalID: id, Action: "things:read"})
	}
	srv, err := runtime.Build(define.NewRegistry().Add(get), runtime.Config{
		Authenticator: authn, Authorizer: authz, RateLimiter: runtime.NewMemoryRateLimiter(c.now),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// roadmap.md M2：提携先ごとのレートリミットが、ほかの提携先に影響しない。
func TestPartnerRateLimitIsPerOrg(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	h := limitedServer(t, c).Handler("partner")
	get := func(token string) int { return call(t, h, "GET", "/things", token, "").status }

	// org_a の2つのサービスアカウントは、同じ Org の上限を分け合う。
	if get("org-a-1") != 200 || get("org-a-2") != 200 {
		t.Fatal("first two calls of org_a must pass")
	}
	res := call(t, h, "GET", "/things", "org-a-1", "")
	if res.status != 429 || res.body["error"] != "resource_exhausted" {
		t.Fatalf("third call of org_a: %d %s", res.status, res.raw)
	}
	// org_b は org_a の上限に影響されない。
	if get("org-b") != 200 || get("org-b") != 200 {
		t.Fatal("org_b must not be limited by org_a")
	}
	// 30秒で1回分戻る（2/min）。
	c.t = c.t.Add(30 * time.Second)
	if get("org-a-1") != 200 || get("org-a-1") != 429 {
		t.Fatal("org_a must get one call back after 30s")
	}
}

func TestRetryAfter(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	h := limitedServer(t, c).Handler("partner")
	call(t, h, "GET", "/things", "org-a-1", "")
	call(t, h, "GET", "/things", "org-a-1", "")
	req := httptest.NewRequest("GET", "/things", nil)
	req.Header.Set("Authorization", "Bearer org-a-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 429 || w.Header().Get("Retry-After") != "30" {
		t.Fatalf("got %d Retry-After=%q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestRateLimitOverGRPC(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	srv := limitedServer(t, c)
	gc := dialGRPC(t, srv, "public")
	if o := gc.call(t, "Things", "Get", "user", `{}`); o.Code != "" {
		t.Fatalf("first call: %+v", o)
	}
	o := gc.call(t, "Things", "Get", "user", `{}`)
	if o.Code != "resource_exhausted" {
		t.Fatalf("second call: %+v", o)
	}
	// REST と gRPC は同じ上限を分け合う（同じ公開範囲・操作・主体）。
	c.t = c.t.Add(time.Second)
	if o := gc.call(t, "Things", "Get", "user", `{}`); o.Code != "" {
		t.Fatalf("after 1s: %+v", o)
	}
	if res := call(t, srv.Handler("public"), "GET", "/things", "user", ""); res.status != 429 {
		t.Fatalf("REST after the gRPC call: %d", res.status)
	}
}

func TestAnonymousIsLimitedPerAddress(t *testing.T) {
	ping := define.Query[struct{}, string]("ping",
		define.Anonymous(),
		define.Expose(define.Public, define.REST),
		define.RateLimit(define.Public, "1/min"),
	).Handle(func(context.Context, struct{}) (string, error) { return "pong", nil })
	srv, err := runtime.Build(define.NewRegistry().Add(ping), runtime.Config{
		Authenticator: memory.NewAuthenticator(), Authorizer: memory.NewAuthorizer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	from := func(addr string) int {
		req := httptest.NewRequest("GET", "/ping", nil)
		req.RemoteAddr = addr
		w := httptest.NewRecorder()
		srv.Handler("public").ServeHTTP(w, req)
		return w.Code
	}
	if from("198.51.100.1:1000") != 200 || from("198.51.100.1:2000") != 429 || from("198.51.100.2:1000") != 200 {
		t.Fatal("anonymous calls must be counted per address, not per port")
	}
}
