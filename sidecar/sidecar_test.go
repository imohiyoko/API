package sidecar_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/imohiyoko/API/adapters/memory"
	"github.com/imohiyoko/API/manifest"
	"github.com/imohiyoko/API/port"
	"github.com/imohiyoko/API/runtime"
	"github.com/imohiyoko/API/sidecar"
)

type upstreamCall struct {
	auth string
	body map[string]any
}

// upstream は本体を真似る。受けた要求を記録し、users.get には見せないフィールドと余計なフィールドも返す。
func upstream(t *testing.T) (*httptest.Server, func() []upstreamCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []upstreamCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		calls = append(calls, upstreamCall{auth: r.Header.Get("Authorization"), body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		in, _ := body["input"].(map[string]any)
		switch r.URL.Path {
		case "/users.get":
			if in["id"] != "usr_1" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no such user","reasons":["missing"]}}`)
				return
			}
			_, _ = io.WriteString(w, `{"output":{"id":"usr_1","name":"Ada","email":"ada@example.com","note":"vip","password_hash":"x"}}`)
		case "/users.list":
			_, _ = io.WriteString(w, `{"output":[{"id":"usr_1","name":"Ada","email":"ada@example.com","note":"vip"}]}`)
		case "/users.create":
			_, _ = io.WriteString(w, `{"output":{"id":"usr_2","name":"wrong"`) // 壊れた JSON
		case "/users.delete":
			_, _ = io.WriteString(w, `{"output":{"id":"usr_1"}}`) // 出力の型（空の object）に無いフィールドは捨てる
		default:
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []upstreamCall { mu.Lock(); defer mu.Unlock(); return append([]upstreamCall(nil), calls...) }
}

func server(t *testing.T, upstreamURL string) *runtime.Server {
	t.Helper()
	f, err := os.Open("../examples/users/api/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := manifest.ReadYAML(f)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := sidecar.Registry(m, sidecar.Config{Upstream: upstreamURL, Token: "sidecar-secret"})
	if err != nil {
		t.Fatal(err)
	}
	authn := memory.NewAuthenticator().
		Add("internal", port.Principal{Type: port.PrincipalServiceAccount, ID: "sva_in"}).
		Add("partner", port.Principal{Type: port.PrincipalServiceAccount, ID: "sva_p", OrgID: "org_p"}).
		Add("user", port.Principal{Type: port.PrincipalUser, ID: "usr_9", SID: "ses_1"})
	authz := memory.NewAuthorizer()
	for _, id := range []string{"sva_in", "sva_p", "usr_9"} {
		for _, a := range []string{"users:read", "users:list", "users:create", "users:delete"} {
			authz.Allow(memory.Grant{PrincipalID: id, Action: a})
		}
	}
	srv, err := runtime.Build(reg, runtime.Config{Authenticator: authn, Authorizer: authz})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func do(t *testing.T, h http.Handler, method, path, token, body string) (int, map[string]any, string) {
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
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

// roadmap.md M3：本体に届く要求に認証済みの主体が載り、絞り込み前のフィールドが本体から漏れない。
func TestUpstreamGetsPrincipalAndCannotLeakFields(t *testing.T) {
	up, calls := upstream(t)
	srv := server(t, up.URL)

	status, out, raw := do(t, srv.Handler("partner"), "GET", "/users/usr_1", "partner", "")
	if status != 200 {
		t.Fatalf("partner get: %d %s", status, raw)
	}
	// partner には email まで。note（min=internal）と、マニフェストに無い password_hash は出さない。
	if out["email"] != "ada@example.com" || out["name"] != "Ada" {
		t.Fatalf("partner get: %s", raw)
	}
	for _, k := range []string{"note", "password_hash"} {
		if _, ok := out[k]; ok {
			t.Fatalf("partner get leaked %s: %s", k, raw)
		}
	}
	got := calls()[0]
	if got.auth != "Bearer sidecar-secret" {
		t.Fatalf("upstream Authorization: %q", got.auth)
	}
	p, _ := got.body["principal"].(map[string]any)
	if got.body["operation"] != "users.get" || got.body["audience"] != "partner" ||
		p["type"] != "service_account" || p["id"] != "sva_p" || p["org_id"] != "org_p" {
		t.Fatalf("upstream request: %+v", got.body)
	}
	if _, ok := p["token"]; ok {
		t.Fatalf("the token must not reach the upstream: %+v", p)
	}

	// public には email も出さない。
	_, out, raw = do(t, srv.Handler("public"), "GET", "/users/usr_1", "user", "")
	if _, ok := out["email"]; ok || out["name"] != "Ada" {
		t.Fatalf("public get: %s", raw)
	}
	// internal にはすべて出る（マニフェストに無いものは出ない）。
	_, out, raw = do(t, srv.Handler("internal"), "GET", "/users/usr_1", "internal", "")
	if out["note"] != "vip" || out["password_hash"] != nil {
		t.Fatalf("internal get: %s", raw)
	}
	// 一覧の要素も絞り込む。
	_, _, raw = do(t, srv.Handler("partner"), "GET", "/users", "partner", "")
	if strings.Contains(raw, "vip") || !strings.Contains(raw, "ada@example.com") {
		t.Fatalf("partner list: %s", raw)
	}
}

func TestInputIsCheckedBeforeTheUpstream(t *testing.T) {
	up, calls := upstream(t)
	srv := server(t, up.URL)
	// partner の口に note（min=internal）を送れば、本体を呼ぶ前に invalid_argument。users.create は partner に出していないので internal で試す。
	status, _, raw := do(t, srv.Handler("internal"), "POST", "/users", "internal", `{"name":"Ada","email":"a@example.com","extra":1}`)
	if status != 400 || !strings.Contains(raw, "unknown field") {
		t.Fatalf("unknown field: %d %s", status, raw)
	}
	status, _, raw = do(t, srv.Handler("internal"), "POST", "/users", "internal", `{"email":"a@example.com"}`)
	if status != 400 || !strings.Contains(raw, "name is required") {
		t.Fatalf("missing field: %d %s", status, raw)
	}
	// 認証・認可で拒んだものも本体に届かない。
	if status, _, _ := do(t, srv.Handler("internal"), "GET", "/users/usr_1", "", ""); status != 401 {
		t.Fatalf("no token: %d", status)
	}
	if status, _, _ := do(t, srv.Handler("internal"), "GET", "/users/usr_1", "user", ""); status != 403 {
		t.Fatalf("user on internal: %d", status)
	}
	if n := len(calls()); n != 0 {
		t.Fatalf("the upstream was called %d times", n)
	}
}

func TestUpstreamErrors(t *testing.T) {
	up, _ := upstream(t)
	srv := server(t, up.URL)
	// 本体の not_found はそのまま。理由は partner なら見える。
	status, out, raw := do(t, srv.Handler("partner"), "GET", "/users/usr_9", "partner", "")
	if status != 404 || out["error"] != "not_found" || !strings.Contains(raw, "missing") {
		t.Fatalf("not found: %d %s", status, raw)
	}
	// 壊れた応答は internal（本体の中身は見せない）。
	status, _, raw = do(t, srv.Handler("internal"), "POST", "/users", "internal", `{"name":"Ada","email":"a@example.com"}`)
	if status != 500 {
		t.Fatalf("broken upstream output: %d %s", status, raw)
	}
	// 型に無いフィールドは捨てる。
	status, _, raw = do(t, srv.Handler("internal"), "DELETE", "/users/usr_1", "internal", "")
	if status != 200 || strings.TrimSpace(raw) != "{}" {
		t.Fatalf("delete: %d %s", status, raw)
	}
}

func TestUnreachableUpstreamIsUnavailable(t *testing.T) {
	up, _ := upstream(t)
	url := up.URL
	up.Close()
	srv := server(t, url)
	if status, _, raw := do(t, srv.Handler("internal"), "GET", "/users/usr_1", "internal", ""); status != 503 {
		t.Fatalf("unreachable upstream: %d %s", status, raw)
	}
}
