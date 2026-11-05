package iam_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
				"aud": []string{"https://users.example"}, "scope": "users:read users:list", "exp": time.Now().Add(time.Hour).Unix()})
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
		case req["principal_type"] == "user" && req["action"] == "users:read" && req["resource_id"] == "usr_1" &&
			req["org_id"] == "org_1" && req["access_token"] == "good":
			_ = json.NewEncoder(w).Encode(map[string]any{"allowed": true})
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
	c, err := iam.New(iam.Config{PublicURL: url, AdminURL: url, ClientID: "rs", ClientSecret: "s3cret", AdminToken: "admin-key", Now: now})
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

	bad, _ := iam.New(iam.Config{PublicURL: srv.URL, AdminURL: srv.URL, ClientID: "rs", ClientSecret: "s3cret", AdminToken: "wrong"})
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
	if err != nil || p.Type != port.PrincipalServiceAccount || p.ID != "sva_1" ||
		len(p.Audiences) != 1 || p.Audiences[0] != "https://users.example" || len(p.Scopes) != 2 {
		t.Fatalf("got %+v %v", p, err)
	}
	if p, err := c.Authenticate(ctx, "sat-one-aud"); err != nil || len(p.Audiences) != 1 || p.Audiences[0] != "https://users.example" {
		t.Fatalf("aud as a string: %+v %v", p, err)
	}
	// client_id の食い違うもの、DPoP に結び付けたもの、Personal Access Token は受けない。
	for _, tok := range []string{"sat-other-client", "dpop", "pat"} {
		if _, err := c.Authenticate(ctx, tok); !errors.Is(err, port.ErrUnauthenticated) {
			t.Fatalf("%s: %v", tok, err)
		}
	}

	// サービスアカウントの判定はスコープで行い、iam の check を呼ばない（imohiyoko/iam#152 まで）。
	d, err := c.Check(ctx, port.CheckRequest{Principal: p, Action: "users:read"})
	if err != nil || !d.Allowed {
		t.Fatalf("users:read: %+v %v", d, err)
	}
	d, err = c.Check(ctx, port.CheckRequest{Principal: p, Action: "users:delete"})
	if err != nil || d.Allowed || len(d.Reasons) != 1 || d.Reasons[0] != "scope_missing" {
		t.Fatalf("users:delete: %+v %v", d, err)
	}
}
