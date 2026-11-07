package iam_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imohiyoko/API/adapters/iam"
	"github.com/imohiyoko/API/port"
)

// fakeIAM は iam のイントロスペクションと authz:check を真似る。
func fakeIAM(t *testing.T, introspections *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/introspect", func(w http.ResponseWriter, r *http.Request) {
		introspections.Add(1)
		id, secret, ok := r.BasicAuth()
		if !ok || id != "rs" || secret != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.PostFormValue("token") {
		case "good":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "usr_1", "org_id": "org_1", "sid": "ses_1", "token_type": "access_token", "exp": time.Now().Add(time.Hour).Unix()})
		case "sat":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "sva_1", "client_id": "sva_1", "token_type": "access_token",
				"principal_type": "service_account", "org_id": "org_2",
				"aud": []string{"https://users.example"}, "scope": "users:read users:list", "exp": time.Now().Add(time.Hour).Unix()})
		case "sat-no-scope":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "sva_1", "client_id": "sva_1", "principal_type": "service_account"})
		case "sat-as-user":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "sva_1", "client_id": "sva_1", "principal_type": "user"})
		case "user-as-sat":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "usr_1", "principal_type": "service_account"})
		case "apikey":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "sva_1", "token_type": "api_key", "principal_type": "service_account"})
		case "odd-type":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "usr_1", "principal_type": "robot"})
		case "sat-one-aud":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "sva_1", "client_id": "sva_1", "aud": "https://users.example"})
		case "sat-other-client":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "sva_1", "client_id": "cli_1"})
		case "dpop":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "sva_1", "client_id": "sva_1", "cnf": map[string]string{"jkt": "x"}})
		case "pat":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "usr_1", "token_type": "personal_access_token"})
		case "refresh":
			_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "sub": "usr_1", "token_type": "refresh_token"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"active": false})
		}
	})
	mux.HandleFunc("POST /v1/admin/authz:check", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch {
		case req["principal_id"] == "usr_missing":
			w.WriteHeader(http.StatusNotFound)
		case req["principal_type"] == "user" && req["action"] == "users-api.users:read" && req["resource_id"] == "usr_1" &&
			req["org_id"] == "org_1" && req["access_token"] == "good":
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": true})
		case req["principal_type"] == "service_account" && req["principal_id"] == "sva_1" && req["action"] == "users-api.users:read" &&
			req["org_id"] == "org_2" && req["access_token"] == "sat":
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": true})
		case req["principal_type"] == "service_account" && req["action"] == "users-api.users:delete":
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": false, "reasons": []string{"inactive"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": false, "unmet": []string{"condition.amr"}})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newClient(t *testing.T, url string, now func() time.Time) *iam.Client {
	t.Helper()
	c, err := iam.New(iam.Config{PublicURL: url, AdminURL: url, ClientID: "rs", ClientSecret: "s3cret", AdminToken: "admin-key", PermissionCatalog: "users-api", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAuthenticate(t *testing.T) {
	var n atomic.Int32
	srv := fakeIAM(t, &n)
	now := time.Now()
	c := newClient(t, srv.URL, func() time.Time { return now })
	ctx := context.Background()

	p, err := c.Authenticate(ctx, "good")
	if err != nil || p.ID != "usr_1" || p.OrgID != "org_1" || p.SID != "ses_1" || p.Type != port.PrincipalUser || p.Token != "good" {
		t.Fatalf("got %+v %v", p, err)
	}
	for _, tok := range []string{"bad", "refresh"} {
		if _, err := c.Authenticate(ctx, tok); !errors.Is(err, port.ErrUnauthenticated) {
			t.Errorf("%s: want ErrUnauthenticated, got %v", tok, err)
		}
	}

	// 結果は CacheTTL（既定30秒）の間だけ持つ。
	before := n.Load()
	_, _ = c.Authenticate(ctx, "good")
	if n.Load() != before {
		t.Error("a cached token was introspected again")
	}
	now = now.Add(31 * time.Second)
	_, _ = c.Authenticate(ctx, "good")
	if n.Load() != before+1 {
		t.Error("an expired cache entry was not introspected again")
	}
}

func TestCheck(t *testing.T) {
	var n atomic.Int32
	srv := fakeIAM(t, &n)
	c := newClient(t, srv.URL, nil)
	ctx := context.Background()
	user := port.Principal{Type: port.PrincipalUser, ID: "usr_1", OrgID: "org_1", Token: "good"}

	d, err := c.Check(ctx, port.CheckRequest{Principal: user, Action: "users:read", ResourceType: "user", ResourceID: "usr_1"})
	if err != nil || !d.Allowed {
		t.Fatalf("got %+v %v", d, err)
	}
	d, err = c.Check(ctx, port.CheckRequest{Principal: user, Action: "users:delete"})
	if err != nil || d.Allowed || len(d.Reasons) != 1 || d.Reasons[0] != "condition.amr" {
		t.Fatalf("got %+v %v", d, err)
	}
	d, err = c.Check(ctx, port.CheckRequest{Principal: port.Principal{Type: port.PrincipalUser, ID: "usr_missing"}, Action: "x:y"})
	if err != nil || d.Allowed {
		t.Fatalf("missing principal: got %+v %v", d, err)
	}

	bad, _ := iam.New(iam.Config{PublicURL: srv.URL, AdminURL: srv.URL, ClientID: "rs", ClientSecret: "s3cret", AdminToken: "wrong", PermissionCatalog: "users-api"})
	if _, err := bad.Check(ctx, port.CheckRequest{Principal: user, Action: "users:read"}); err == nil {
		t.Fatal("want an error when iam rejects the admin token")
	}
}

func TestServiceAccountToken(t *testing.T) {
	var n atomic.Int32
	srv := fakeIAM(t, &n)
	c := newClient(t, srv.URL, nil)
	ctx := context.Background()

	p, err := c.Authenticate(ctx, "sat")
	if err != nil || p.Type != port.PrincipalServiceAccount || p.ID != "sva_1" || p.OrgID != "org_2" || p.SID != "" ||
		len(p.Audiences) != 1 || p.Audiences[0] != "https://users.example" || len(p.Scopes) != 2 {
		t.Fatalf("got %+v %v", p, err)
	}
	if p, err := c.Authenticate(ctx, "sat-one-aud"); err != nil || len(p.Audiences) != 1 || p.Audiences[0] != "https://users.example" {
		t.Fatalf("aud as a string: %+v %v", p, err)
	}
	// client_id の食い違うもの、DPoP に結び付けたもの、Personal Access Token、API キー、主体の種類が ID と食い違うか知らないものは受けない。
	for _, tok := range []string{"sat-other-client", "dpop", "pat", "sat-as-user", "user-as-sat", "odd-type", "apikey"} {
		if _, err := c.Authenticate(ctx, tok); !errors.Is(err, port.ErrUnauthenticated) {
			t.Fatalf("%s: %v", tok, err)
		}
	}

	// サービスアカウントも iam の check で判定する（主体の種類・Org・トークンを渡す。imohiyoko/iam#152）。
	d, err := c.Check(ctx, port.CheckRequest{Principal: p, Action: "users:read"})
	if err != nil || !d.Allowed {
		t.Fatalf("users:read: %+v %v", d, err)
	}
	// トークンのスコープに無い操作は、iam を呼ばずに拒む。
	d, err = c.Check(ctx, port.CheckRequest{Principal: p, Action: "users:delete"})
	if err != nil || d.Allowed || len(d.Reasons) != 1 || d.Reasons[0] != "scope_missing" {
		t.Fatalf("users:delete: %+v %v", d, err)
	}
	// スコープの無いトークンは iam の判定だけで決まる。
	q, err := c.Authenticate(ctx, "sat-no-scope")
	if err != nil {
		t.Fatal(err)
	}
	d, err = c.Check(ctx, port.CheckRequest{Principal: q, Action: "users:delete"})
	if err != nil || d.Allowed || len(d.Reasons) != 1 || d.Reasons[0] != "inactive" {
		t.Fatalf("no scope: %+v %v", d, err)
	}
}

func TestPermissionCatalog(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = req["action"]
		_ = json.NewEncoder(w).Encode(map[string]any{"allowed": true})
	}))
	t.Cleanup(srv.Close)
	cfg := iam.Config{PublicURL: srv.URL, AdminURL: srv.URL, ClientID: "rs", ClientSecret: "s", AdminToken: "a", PermissionCatalog: "users-api"}
	c, err := iam.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// スコープは接頭辞を付けずに比べ、iam の check にはカタログの権限の名前で渡す。
	sa := port.Principal{Type: port.PrincipalServiceAccount, ID: "sva_1", Scopes: []string{"users:read"}}
	if d, err := c.Check(context.Background(), port.CheckRequest{Principal: sa, Action: "users:read"}); err != nil || !d.Allowed || got != "users-api.users:read" {
		t.Fatalf("got %+v %v, action %q", d, err, got)
	}
	// カタログは必須（無ければ連携先の権限の名前にできない）で、iam の接頭辞の形に限る。
	for _, bad := range []string{"", "Users", "1x", "users.api", "users_api", "a b", strings.Repeat("a", 33)} {
		cfg.PermissionCatalog = bad
		if _, err := iam.New(cfg); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}
