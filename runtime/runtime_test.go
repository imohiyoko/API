package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/define"
	"github.com/imohiyoko/API/examples/users"
	"github.com/imohiyoko/API/port"
	"github.com/imohiyoko/API/runtime"
)

func newServer(t *testing.T) (*runtime.Server, *users.Store) {
	t.Helper()
	store := &users.Store{}
	authn := memory.NewAuthenticator().
		Add("internal", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_int"}).
		Add("partner", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_par", OrgID: "org_1"}).
		Add("partner-no-org", port.Principal{Type: port.PrincipalServiceAccount, ID: "sa_par"}).
		Add("user", port.Principal{Type: port.PrincipalUser, ID: "usr_1"}).
		Add("user-denied", port.Principal{Type: port.PrincipalUser, ID: "usr_2"})
	authz := memory.NewAuthorizer()
	for _, id := range []string{"sa_int", "sa_par", "usr_1"} {
		for _, a := range []string{"users:read", "users:list", "users:create", "users:delete"} {
			authz.Allow(memory.Grant{PrincipalID: id, Action: a})
		}
	}
	srv, err := runtime.Build(users.Registry(store), runtime.Config{Authenticator: authn, Authorizer: authz})
	if err != nil {
		t.Fatal(err)
	}
	return srv, store
}

type response struct {
	status int
	body   map[string]any
	raw    string
}

func call(t *testing.T, h http.Handler, method, path, token, body string) response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	res := response{status: w.Code, raw: w.Body.String()}
	_ = json.Unmarshal(w.Body.Bytes(), &res.body)
	return res
}

func create(t *testing.T, srv *runtime.Server) string {
	t.Helper()
	res := call(t, srv.Handler("internal"), "POST", "/users", "internal", `{"name":"Ada","email":"ada@example.com","note":"vip"}`)
	if res.status != 200 {
		t.Fatalf("create: %d %s", res.status, res.raw)
	}
	return res.body["id"].(string)
}

// roadmap.md M1：min=partner のフィールドが public の応答に出ない。
func TestFieldsAreProjectedPerAudience(t *testing.T) {
	srv, _ := newServer(t)
	id := create(t, srv)
	cases := []struct {
		aud, token string
		want       []string
		absent     []string
	}{
		{"internal", "internal", []string{"id", "name", "email", "note"}, nil},
		{"partner", "partner", []string{"id", "name", "email"}, []string{"note"}},
		{"public", "user", []string{"id", "name"}, []string{"email", "note"}},
	}
	for _, c := range cases {
		res := call(t, srv.Handler(c.aud), "GET", "/users/"+id, c.token, "")
		if res.status != 200 {
			t.Fatalf("%s: %d %s", c.aud, res.status, res.raw)
		}
		for _, k := range c.want {
			if _, ok := res.body[k]; !ok {
				t.Errorf("%s: missing %q in %s", c.aud, k, res.raw)
			}
		}
		for _, k := range c.absent {
			if _, ok := res.body[k]; ok {
				t.Errorf("%s: %q must not be visible: %s", c.aud, k, res.raw)
			}
		}
	}
}

// roadmap.md M1：public の口で、internal にだけ出した操作が 404 になる。
func TestOperationsExistOnlyOnExposedAudiences(t *testing.T) {
	srv, _ := newServer(t)
	if res := call(t, srv.Handler("public"), "POST", "/users", "user", `{"name":"x","email":"y"}`); res.status != 404 && res.status != 405 {
		t.Errorf("public POST /users: got %d, want 404/405", res.status)
	}
	if res := call(t, srv.Handler("public"), "GET", "/users", "user", ""); res.status != 404 {
		t.Errorf("public GET /users (internal and partner only): got %d, want 404", res.status)
	}
	if res := call(t, srv.Handler("nope"), "GET", "/users", "user", ""); res.status != 404 {
		t.Errorf("unknown audience: got %d", res.status)
	}
}

// roadmap.md M1：Expose の無い操作がどの口にも出ない。
func TestUnexposedOperationIsNowhere(t *testing.T) {
	hidden := define.Query[struct{}, string]("secret.get", define.Authz("secret:read")).
		Handle(func(context.Context, struct{}) (string, error) { return "s", nil })
	reg := define.NewRegistry().Add(hidden)
	srv, err := runtime.Build(reg, runtime.Config{Authenticator: memory.NewAuthenticator(), Authorizer: memory.NewAuthorizer()})
	if err != nil {
		t.Fatal(err)
	}
	for _, aud := range []string{"internal", "partner", "public"} {
		if res := call(t, srv.Handler(aud), "GET", "/secret", "", ""); res.status != 404 {
			t.Errorf("%s: got %d, want 404", aud, res.status)
		}
	}
}

func TestAuthentication(t *testing.T) {
	srv, _ := newServer(t)
	id := create(t, srv)
	cases := []struct {
		name, aud, token string
		status           int
		code             string
	}{
		{"no token", "public", "", 401, "unauthenticated"},
		{"unknown token", "public", "bogus", 401, "unauthenticated"},
		{"user on internal", "internal", "user", 403, "permission_denied"},
		{"partner without org", "partner", "partner-no-org", 403, "permission_denied"},
		{"denied by authz", "public", "user-denied", 403, "permission_denied"},
	}
	for _, c := range cases {
		res := call(t, srv.Handler(c.aud), "GET", "/users/"+id, c.token, "")
		if res.status != c.status || res.body["error"] != c.code {
			t.Errorf("%s: got %d %s", c.name, res.status, res.raw)
		}
	}
}

// design.md 4.1：エラーの詳しさは公開範囲で変わる。
func TestErrorDisclosure(t *testing.T) {
	srv, _ := newServer(t)
	internal := call(t, srv.Handler("internal"), "GET", "/users/usr_x", "internal", "")
	if internal.status != 404 || internal.body["message"] == nil {
		t.Errorf("internal should see the message: %s", internal.raw)
	}
	public := call(t, srv.Handler("public"), "GET", "/users/usr_x", "user", "")
	if public.status != 404 || public.body["message"] != nil {
		t.Errorf("public should see only the code: %s", public.raw)
	}
	denied := call(t, srv.Handler("public"), "GET", "/users/usr_x", "user-denied", "")
	if denied.body["reasons"] != nil {
		t.Errorf("public should not see reasons: %s", denied.raw)
	}
	wrong := call(t, srv.Handler("internal"), "GET", "/users/usr_x", "user", "")
	if wrong.body["reasons"] == nil {
		t.Errorf("internal should see reasons: %s", wrong.raw)
	}
}

func TestInputValidation(t *testing.T) {
	srv, _ := newServer(t)
	h := srv.Handler("internal")
	cases := []struct{ name, method, path, body string }{
		{"missing required", "POST", "/users", `{"name":"x"}`},
		{"unknown field", "POST", "/users", `{"name":"x","email":"y","admin":true}`},
		{"wrong type", "POST", "/users", `{"name":1,"email":"y"}`},
		{"not an object", "POST", "/users", `[1]`},
		{"bad query", "GET", "/users?limit=abc", ""},
		{"repeated query", "GET", "/users?limit=1&limit=2", ""},
	}
	for _, c := range cases {
		res := call(t, h, c.method, c.path, "internal", c.body)
		if res.status != 400 || res.body["error"] != "invalid_argument" || res.body["message"] == nil {
			t.Errorf("%s: got %d %s", c.name, res.status, res.raw)
		}
	}
	if res := call(t, h, "POST", "/users", "internal", `{"name":"x","email":"y","note":"n"}`); res.status != 200 {
		t.Errorf("internal may set note: %s", res.raw)
	}
}

func TestQueryAndList(t *testing.T) {
	srv, _ := newServer(t)
	create(t, srv)
	create(t, srv)
	res := call(t, srv.Handler("partner"), "GET", "/users?limit=1", "partner", "")
	var list []map[string]any
	if err := json.Unmarshal([]byte(res.raw), &list); err != nil || res.status != 200 || len(list) != 1 {
		t.Fatalf("got %d %s", res.status, res.raw)
	}
	if _, ok := list[0]["note"]; ok {
		t.Errorf("partner list must not show note: %s", res.raw)
	}
	if res := call(t, srv.Handler("internal"), "DELETE", "/users/"+list[0]["id"].(string), "internal", ""); res.status != 200 {
		t.Errorf("delete: %d %s", res.status, res.raw)
	}
}

func TestBuildRejectsInvalidDefinitions(t *testing.T) {
	bad := define.Query[struct{}, string]("open.get", define.Expose(define.Public, define.REST)).
		Handle(func(context.Context, struct{}) (string, error) { return "", nil })
	_, err := runtime.Build(define.NewRegistry().Add(bad), runtime.Config{Authenticator: memory.NewAuthenticator(), Authorizer: memory.NewAuthorizer()})
	var ce *runtime.CheckError
	if !errors.As(err, &ce) {
		t.Fatalf("want CheckError, got %v", err)
	}
	noHandler := define.Query[struct{}, string]("x.get", define.Anonymous(), define.Expose(define.Public, define.REST))
	if _, err := runtime.Build(define.NewRegistry().Add(noHandler), runtime.Config{Authenticator: memory.NewAuthenticator(), Authorizer: memory.NewAuthorizer()}); err == nil {
		t.Fatal("want an error for a missing handler")
	}
}

func TestAnonymousAndPanics(t *testing.T) {
	health := define.Query[struct{}, string]("health.get", define.Anonymous(), define.Expose(define.Public, define.REST)).
		Handle(func(ctx context.Context, _ struct{}) (string, error) {
			if _, ok := define.PrincipalFrom(ctx); ok {
				return "authenticated", nil
			}
			return "ok", nil
		})
	boom := define.Query[struct{}, string]("boom.get", define.Anonymous(), define.Expose(define.Public, define.REST)).
		Handle(func(context.Context, struct{}) (string, error) { panic("x") })
	authn := memory.NewAuthenticator().Add("user", port.Principal{Type: port.PrincipalUser, ID: "u"})
	srv, err := runtime.Build(define.NewRegistry().Add(health, boom), runtime.Config{Authenticator: authn, Authorizer: memory.NewAuthorizer()})
	if err != nil {
		t.Fatal(err)
	}
	if res := call(t, srv.Handler("public"), "GET", "/health", "", ""); res.status != 200 || res.raw != "\"ok\"\n" {
		t.Errorf("anonymous: %d %q", res.status, res.raw)
	}
	if res := call(t, srv.Handler("public"), "GET", "/health", "user", ""); res.raw != "\"authenticated\"\n" {
		t.Errorf("anonymous with token: %q", res.raw)
	}
	if res := call(t, srv.Handler("public"), "GET", "/health", "bogus", ""); res.status != 401 {
		t.Errorf("anonymous with a bad token: %d", res.status)
	}
	if res := call(t, srv.Handler("public"), "GET", "/boom", "", ""); res.status != 500 || res.body["message"] != nil {
		t.Errorf("panic: %d %s", res.status, res.raw)
	}
}
